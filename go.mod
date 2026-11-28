module github.com/snowmerak/q/v2

go 1.27.4

ignore (
	bin
	desktop
	studio
)

require (
	github.com/chromedp/cdproto v0.159.0
	github.com/chromedp/chromedp v0.23.0
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/fsnotify/fsnotify v1.10.1
	github.com/redis/rueidis v1.0.80
	github.com/snowmerak/decisions v1.0.0
	golang.org/x/sys v0.50.0
)

require (
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/go-json-experiment/json v0.0.0-20260213210345-44df1a37e875 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
)
