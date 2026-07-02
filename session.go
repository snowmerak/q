package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

type Session struct {
	Name      string        `json:"name"`
	UpdatedAt time.Time     `json:"updated_at"`
	PWD       string        `json:"pwd"`
	Messages  []ChatMessage `json:"messages"`
}

func getSessionDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	sessionDir := filepath.Join(configDir, "q", "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", err
	}
	return sessionDir, nil
}

func getSessionPath(name string) (string, error) {
	dir, err := getSessionDir()
	if err != nil {
		return "", err
	}
	safeName := filepath.Base(name)
	if !strings.HasSuffix(safeName, ".json") {
		safeName = safeName + ".json"
	}
	return filepath.Join(dir, safeName), nil
}

func LoadSession(name string) (*Session, error) {
	filePath, err := getSessionPath(name)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		wd, err := os.Getwd()
		if err != nil {
			wd = "."
		}
		return &Session{
			Name:      name,
			UpdatedAt: time.Now(),
			PWD:       wd,
			Messages:  []ChatMessage{},
		}, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}

	return &s, nil
}

func SaveSession(s *Session) error {
	s.UpdatedAt = time.Now()
	filePath, err := getSessionPath(s.Name)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0644)
}

func ListSessions() error {
	dir, err := getSessionDir()
	if err != nil {
		return err
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\n", Bold("SESSION NAME"), Bold("LAST ACTIVE"), Bold("LAST PWD"))
	fmt.Fprintf(w, "%s\t%s\t%s\n", "------------", "-----------", "--------")

	count := 0
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			name := strings.TrimSuffix(file.Name(), ".json")
			s, err := LoadSession(name)
			if err != nil {
				continue
			}
			count++
			timeStr := s.UpdatedAt.Format("2006-01-02 15:04:05")
			fmt.Fprintf(w, "%s\t%s\t%s\n", Color(s.Name, ansiCyan), Dim(timeStr), Color(s.PWD, ansiBlue))
		}
	}
	_ = w.Flush()

	if count == 0 {
		PrintWarning("No active sessions found.")
	}

	return nil
}

func DeleteSession(name string) error {
	filePath, err := getSessionPath(name)
	if err != nil {
		return err
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("session '%s' does not exist", name)
	}

	if err := os.Remove(filePath); err != nil {
		return err
	}

	PrintSuccess(fmt.Sprintf("Session '%s' deleted successfully.", name))
	return nil
}

func RenameSession(oldName, newName string) error {
	s, err := LoadSession(oldName)
	if err != nil {
		return err
	}
	
	oldPath, _ := getSessionPath(oldName)
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		return fmt.Errorf("session '%s' does not exist", oldName)
	}

	_ = os.Remove(oldPath)

	s.Name = newName
	if err := SaveSession(s); err != nil {
		return err
	}

	PrintSuccess(fmt.Sprintf("Session '%s' renamed to '%s'.", oldName, newName))
	return nil
}

func CopySession(srcName, dstName string) error {
	s, err := LoadSession(srcName)
	if err != nil {
		return err
	}
	
	srcPath, _ := getSessionPath(srcName)
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return fmt.Errorf("source session '%s' does not exist", srcName)
	}

	s.Name = dstName
	if err := SaveSession(s); err != nil {
		return err
	}

	PrintSuccess(fmt.Sprintf("Session '%s' copied to '%s'.", srcName, dstName))
	return nil
}
