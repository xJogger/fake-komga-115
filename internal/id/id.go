package id

import (
	"encoding/base64"
	"errors"
	"strings"
)

func Library(rootCID string) string       { return encode("l_", rootCID) }
func Series(libraryID, cid string) string { return encode("s_", libraryID+"\x00"+cid) }
func OneShotSeries(libraryID, fileID string) string {
	return Series(libraryID, OneShotCID(fileID))
}
func OneShotCID(fileID string) string      { return "file:" + fileID }
func Book(libraryID, fileID string) string { return encode("b_", libraryID+"\x00"+fileID) }

func DecodeSeries(value string) (libraryID, cid string, err error) {
	return decodePair("s_", value)
}

func DecodeBook(value string) (libraryID, fileID string, err error) {
	return decodePair("b_", value)
}

func encode(prefix, value string) string {
	return prefix + base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decode(prefix, value string) (string, error) {
	if !strings.HasPrefix(value, prefix) {
		return "", errors.New("invalid id prefix")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || len(raw) == 0 {
		return "", errors.New("invalid id")
	}
	return string(raw), nil
}

func decodePair(prefix, value string) (string, string, error) {
	raw, err := decode(prefix, value)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(raw, "\x00", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("invalid composite id")
	}
	return parts[0], parts[1], nil
}
