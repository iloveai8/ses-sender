// Package httpx 横切 HTTP 件：统一错误体（契约核心）与 pydantic 422 复刻。
// 契约：错误一律 {"detail": ...}；结构体字段顺序即 JSON 输出顺序（golden 锚定，禁用 map 渲染）。
package httpx

import "fmt"

// Error 业务层唯一错误出口；Detail 为 string（普通错误）或 []ValidationIssue（422）
type Error struct {
	Status int
	Detail any
}

func New(status int, detail string) *Error {
	return &Error{Status: status, Detail: detail}
}

func (e *Error) Error() string {
	if s, ok := e.Detail.(string); ok {
		return s
	}
	return fmt.Sprint(e.Detail)
}

// ValidationIssue pydantic v2 校验错误项（字段顺序=golden 契约：type/loc/msg/input）
type ValidationIssue struct {
	Type  string   `json:"type"`
	Loc   []string `json:"loc"`
	Msg   string   `json:"msg"`
	Input any      `json:"input"`
}

// UnprocessableMissing 复刻 pydantic "Field required"：loc=["body",字段名]，input=原始请求体
func UnprocessableMissing(field string, input any) *Error {
	return &Error{
		Status: 422,
		Detail: []ValidationIssue{{
			Type:  "missing",
			Loc:   []string{"body", field},
			Msg:   "Field required",
			Input: input,
		}},
	}
}

// 常用错误（文案=golden 锚定，逐字对齐 Python 版）
func ErrNotAuthenticated() *Error { return New(401, "Not authenticated") }
func ErrInvalidAuth() *Error      { return New(401, "无效的认证信息") }
func ErrUserDisabled() *Error     { return New(401, "用户不存在或已禁用") }
func ErrRequireAdmin() *Error     { return New(403, "需要管理员权限") }
func ErrNotFound() *Error         { return New(404, "Not Found") }
