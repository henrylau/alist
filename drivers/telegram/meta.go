package telegram

import (
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/op"
)

type Addition struct {
	// Usually one of two
	// driver.RootPath
	// driver.RootID
	// define other
	PhoneNumber string `json:"phone_number"`
	AuthCode    string `json:"auth_code"`
	ApiId       string `json:"api_id"`
	ApiHash     string `json:"api_hash"`
	Session     string `json:"session"`
	driver.RootID
}

var config = driver.Config{
	Name:      "Telegram",
	LocalSort: false,
	OnlyLocal: false,
	OnlyProxy: true,
	NoCache:   false,
	NoUpload:  true,
	NeedMs:    false,
	// DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Telegram{}
	})
}
