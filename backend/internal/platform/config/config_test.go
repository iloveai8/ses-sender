package config

import "testing"

// TestDSN 连接串翻译矩阵（pymysql 存量格式 → go-sql-driver；Go 原生直通）
func TestDSN(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"mysql+pymysql://root:123@localhost:3306/db",
			"root:123@tcp(localhost:3306)/db?charset=utf8mb4&parseTime=true&loc=UTC",
		},
		{
			"root:123@tcp(localhost:3306)/db",
			"root:123@tcp(localhost:3306)/db?charset=utf8mb4&parseTime=true&loc=UTC",
		},
		{
			"mysql+pymysql://u:p@h:3306/db?x=1",
			"u:p@tcp(h:3306)/db?x=1&charset=utf8mb4&parseTime=true&loc=UTC",
		},
	}
	for _, c := range cases {
		if got := (Database{URL: c.in}).DSN(); got != c.want {
			t.Errorf("DSN(%q)\n got  %s\n want %s", c.in, got, c.want)
		}
	}
}
