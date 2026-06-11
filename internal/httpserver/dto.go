package httpserver

type pageResponse[T any] struct {
	Content          []T        `json:"content"`
	Pageable         pageable   `json:"pageable"`
	TotalElements    int64      `json:"totalElements"`
	TotalPages       int        `json:"totalPages"`
	Last             bool       `json:"last"`
	Size             int        `json:"size"`
	Number           int        `json:"number"`
	Sort             sortObject `json:"sort"`
	NumberOfElements int        `json:"numberOfElements"`
	First            bool       `json:"first"`
	Empty            bool       `json:"empty"`
}

type pageable struct {
	Sort       sortObject `json:"sort"`
	Offset     int        `json:"offset"`
	PageNumber int        `json:"pageNumber"`
	PageSize   int        `json:"pageSize"`
	Paged      bool       `json:"paged"`
	Unpaged    bool       `json:"unpaged"`
}

type sortObject struct {
	Empty    bool `json:"empty"`
	Sorted   bool `json:"sorted"`
	Unsorted bool `json:"unsorted"`
}

func makePage[T any](content []T, page, size int, total int64, unpaged bool) pageResponse[T] {
	if size <= 0 {
		size = 20
	}
	totalPages := int((total + int64(size) - 1) / int64(size))
	if unpaged {
		page = 0
		size = len(content)
		totalPages = 1
		if total == 0 {
			totalPages = 0
		}
	}
	sortInfo := sortObject{Empty: true, Unsorted: true}
	return pageResponse[T]{
		Content: content,
		Pageable: pageable{
			Sort: sortInfo, Offset: page * size, PageNumber: page, PageSize: size,
			Paged: !unpaged, Unpaged: unpaged,
		},
		TotalElements: total, TotalPages: totalPages,
		Last: page+1 >= totalPages, Size: size, Number: page, Sort: sortInfo,
		NumberOfElements: len(content), First: page == 0, Empty: len(content) == 0,
	}
}
