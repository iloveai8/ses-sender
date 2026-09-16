package httpx

// Page 分页响应包装（字段顺序=契约锚定：items,total,page,page_size,total_pages；
// 例外：/admin/blacklist 缺 page_size——届时单独 DTO，勿改本结构）
type Page[T any] struct {
	Items      []T `json:"items"`
	Total      int `json:"total"`
	PageNum    int `json:"page"`
	PageSize   int `json:"page_size"`
	TotalPages int `json:"total_pages"`
}

// NewPage 组装分页响应；total_pages = max(1, ceil(total/page_size))——Python 口径
func NewPage[T any](items []T, total, page, pageSize int) Page[T] {
	tp := 1
	if pageSize > 0 {
		if v := (total + pageSize - 1) / pageSize; v > 1 {
			tp = v
		}
	}
	return Page[T]{Items: items, Total: total, PageNum: page, PageSize: pageSize, TotalPages: tp}
}
