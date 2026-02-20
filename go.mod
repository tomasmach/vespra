module github.com/tomasmach/mnemon-bot

go 1.24

// CGO is required by github.com/mattn/go-sqlite3
require (
	github.com/BurntSushi/toml v1.6.0
	github.com/bwmarrin/discordgo v0.29.0
	github.com/mattn/go-sqlite3 v1.14.34
)

require (
	github.com/gorilla/websocket v1.4.2 // indirect
	golang.org/x/crypto v0.0.0-20210421170649-83a5a9bb288b // indirect
	golang.org/x/sys v0.0.0-20201119102817-f84b799fce68 // indirect
)
