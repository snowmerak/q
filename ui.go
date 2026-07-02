package main

import "fmt"

const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiDim     = "\033[2m"
	ansiRed     = "\033[31m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiBlue    = "\033[34m"
	ansiMagenta = "\033[35m"
	ansiCyan    = "\033[36m"
)

func Color(text string, ansiCode string) string {
	return ansiCode + text + ansiReset
}

func Bold(text string) string {
	return ansiBold + text + ansiReset
}

func Dim(text string) string {
	return ansiDim + text + ansiReset
}

func PrintInfo(text string) {
	fmt.Println(Color("✨ "+text, ansiCyan))
}

func PrintSuccess(text string) {
	fmt.Println(Color("✅ "+text, ansiGreen))
}

func PrintWarning(text string) {
	fmt.Println(Color("⚠️  "+text, ansiYellow))
}

func PrintError(text string) {
	fmt.Println(Color("❌ "+text, ansiRed))
}

func PrintAgentThinking() {
	fmt.Print(Color("🤖 Thinking...", ansiMagenta) + "\r")
}

func ClearAgentThinking() {
	fmt.Print("               \r")
}
