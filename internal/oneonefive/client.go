package oneonefive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"golang.org/x/time/rate"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const UserAgent = "fake-komga-115/0.1"

type File struct {
	ID         string    `json:"id"`
	ParentID   string    `json:"parentId"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	PickCode   string    `json:"pickCode"`
	SHA1       string    `json:"sha1"`
	IsDir      bool      `json:"isDir"`
	CreatedAt  time.Time `json:"createdAt"`
	ModifiedAt time.Time `json:"modifiedAt"`
	Incomplete bool      `json:"incomplete"`
}

type Folder struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PickCode string `json:"pickCode"`
}

type Download struct {
	URL       string
	UserAgent string
}

type ConnectionInfo struct {
	UserID   int64  `json:"userId"`
	UserName string `json:"userName"`
	VIP      string `json:"vip"`
}

type Client struct {
	store  *database.Store
	logger *slog.Logger
	http   *http.Client

	mu      sync.RWMutex
	sdk     *sdk.Client
	limiter *rate.Limiter
}

func New(store *database.Store, logger *slog.Logger) *Client {
	c := &Client{
		store:  store,
		logger: logger,
		http: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
	c.Reload(context.Background())
	return c
}

func (c *Client) Reload(ctx context.Context) {
	account, err := c.store.Account(ctx)
	if err != nil {
		c.logger.Error("load 115 account", "error", err)
		return
	}
	requestRate := c.store.Float64Setting(ctx, "api_rate_per_second", 1)
	if requestRate <= 0 {
		requestRate = 1
	}
	client := sdk.New(
		sdk.WithAccessToken(account.AccessToken),
		sdk.WithRefreshToken(account.RefreshToken),
		sdk.WithOnRefreshToken(func(accessToken, refreshToken string) {
			if err := c.store.SaveAccount(context.Background(), accessToken, refreshToken); err != nil {
				c.logger.Error("persist refreshed 115 token", "error", err)
			}
		}),
	)
	client.SetHttpClient(c.http).SetUserAgent(UserAgent)
	c.mu.Lock()
	c.sdk = client
	c.limiter = rate.NewLimiter(rate.Limit(requestRate), 1)
	c.mu.Unlock()
}

func (c *Client) SaveAndTest(ctx context.Context, accessToken, refreshToken string) (ConnectionInfo, error) {
	if refreshToken == "" {
		return ConnectionInfo{}, errors.New("refresh token is required")
	}
	currentAccess, currentRefresh := accessToken, refreshToken
	client := sdk.New(
		sdk.WithAccessToken(accessToken),
		sdk.WithRefreshToken(refreshToken),
		sdk.WithOnRefreshToken(func(newAccess, newRefresh string) {
			currentAccess, currentRefresh = newAccess, newRefresh
		}),
	)
	client.SetHttpClient(c.http).SetUserAgent(UserAgent)
	if accessToken == "" {
		if _, err := client.RefreshToken(ctx); err != nil {
			return ConnectionInfo{}, fmt.Errorf("refresh token: %w", err)
		}
	}
	info, err := client.UserInfo(ctx)
	if err != nil {
		return ConnectionInfo{}, fmt.Errorf("get user info: %w", err)
	}
	if err := c.store.SaveAccount(ctx, currentAccess, currentRefresh); err != nil {
		return ConnectionInfo{}, err
	}
	c.Reload(ctx)
	return ConnectionInfo{
		UserID: info.UserID, UserName: info.UserName, VIP: info.VipInfo.LevelName,
	}, nil
}

func (c *Client) Test(ctx context.Context) (ConnectionInfo, error) {
	client, err := c.current()
	if err != nil {
		return ConnectionInfo{}, err
	}
	account, err := c.store.Account(ctx)
	if err != nil {
		return ConnectionInfo{}, err
	}
	if account.AccessToken == "" {
		if err := c.wait(ctx); err != nil {
			return ConnectionInfo{}, err
		}
		if _, err := client.RefreshToken(ctx); err != nil {
			return ConnectionInfo{}, fmt.Errorf("refresh token: %w", err)
		}
	}
	if err := c.wait(ctx); err != nil {
		return ConnectionInfo{}, err
	}
	info, err := client.UserInfo(ctx)
	if err != nil {
		return ConnectionInfo{}, fmt.Errorf("get user info: %w", err)
	}
	return ConnectionInfo{
		UserID:   info.UserID,
		UserName: info.UserName,
		VIP:      info.VipInfo.LevelName,
	}, nil
}

func (c *Client) RefreshToken(ctx context.Context) error {
	client, err := c.current()
	if err != nil {
		return err
	}
	if err := c.wait(ctx); err != nil {
		return err
	}
	_, err = client.RefreshToken(ctx)
	return err
}

func (c *Client) FolderInfo(ctx context.Context, cid string) (Folder, error) {
	if cid == "0" {
		return Folder{ID: "0", Name: "115"}, nil
	}
	client, err := c.current()
	if err != nil {
		return Folder{}, err
	}
	if err := c.wait(ctx); err != nil {
		return Folder{}, err
	}
	info, err := client.GetFolderInfo(ctx, cid)
	if err != nil {
		return Folder{}, err
	}
	return Folder{ID: info.FileID, Name: info.FileName, PickCode: info.PickCode}, nil
}

func (c *Client) ListDirectory(ctx context.Context, cid string) ([]File, error) {
	client, err := c.current()
	if err != nil {
		return nil, err
	}
	const pageSize int64 = 1150
	var out []File
	for offset := int64(0); ; offset += pageSize {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		response, err := client.GetFiles(ctx, &sdk.GetFilesReq{
			CID:     cid,
			Limit:   pageSize,
			Offset:  offset,
			ASC:     true,
			O:       "file_name",
			ShowDir: true,
			Cur:     1,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range response.Data {
			out = append(out, File{
				ID:         item.Fid,
				ParentID:   item.Pid,
				Name:       item.Fn,
				Size:       item.FS,
				PickCode:   item.Pc,
				SHA1:       item.Sha1,
				IsDir:      item.Fc == "0",
				CreatedAt:  time.Unix(item.UpPt, 0).UTC(),
				ModifiedAt: time.Unix(item.Upt, 0).UTC(),
				Incomplete: item.Fta != "" && item.Fta != "1",
			})
		}
		if int64(len(out)) >= response.Count || len(response.Data) == 0 {
			break
		}
	}
	return out, nil
}

func (c *Client) DownloadURL(ctx context.Context, fileID, pickCode, userAgent string) (Download, error) {
	client, err := c.current()
	if err != nil {
		return Download{}, err
	}
	if userAgent == "" {
		userAgent = UserAgent
	}
	if err := c.wait(ctx); err != nil {
		return Download{}, err
	}
	response, err := client.DownURL(ctx, pickCode, userAgent)
	if err != nil {
		return Download{}, err
	}
	item, ok := response[fileID]
	if !ok || item.URL.URL == "" {
		return Download{}, errors.New("115 returned no download URL for file")
	}
	return Download{URL: item.URL.URL, UserAgent: userAgent}, nil
}

func (c *Client) current() (*sdk.Client, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.sdk == nil {
		return nil, errors.New("115 client is not configured")
	}
	return c.sdk, nil
}

func (c *Client) wait(ctx context.Context) error {
	c.mu.RLock()
	limiter := c.limiter
	c.mu.RUnlock()
	if limiter == nil {
		return nil
	}
	return limiter.Wait(ctx)
}
