

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	wishtea "github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
)

const filePath = "messages.txt"

var fileMu sync.Mutex

var (
	primaryColor   = lipgloss.Color("205")
	secondaryColor = lipgloss.Color("86")
	subtleColor    = lipgloss.Color("241")
	accentColor    = lipgloss.Color("212")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(primaryColor).
			MarginBottom(1)

	userTagStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			Bold(true)

	timestampStyle = lipgloss.NewStyle().
			Foreground(subtleColor)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primaryColor).
			Padding(1, 2).
			Width(64)

	inputContainerStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(secondaryColor).
				Padding(0, 1).
				MarginTop(1)

	footerStyle = lipgloss.NewStyle().
			Foreground(subtleColor).
			Italic(true).
			MarginTop(1)

	successStyle = lipgloss.NewStyle().
			Foreground(accentColor).
			Bold(true).
			Padding(1, 2)
)

type model struct {
	username  string
	messages  []string
	textInput textinput.Model
	err       error
	submitted bool
}

func loadMessages() ([]string, error) {
	fileMu.Lock()
	defer fileMu.Unlock()

	file, err := os.OpenFile(filePath, os.O_RDONLY|os.O_CREATE, 0666)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	return lines, scanner.Err()
}

func saveMessage(user, msg string) error {
	fileMu.Lock()
	defer fileMu.Unlock()

	file, err := os.OpenFile(
		filePath,
		os.O_WRONLY|os.O_CREATE|os.O_APPEND,
		0666,
	)
	if err != nil {
		return err
	}
	defer file.Close()

	timestamp := time.Now().Format("2006-01-02 15:04")
	entry := fmt.Sprintf(
		"[%s] %s: %s",
		timestamp,
		user,
		strings.TrimSpace(msg),
	)

	_, err = file.WriteString(entry + "\n")
	return err
}

func messagesAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	switch r.Method {
	case http.MethodGet:
		msgs, err := loadMessages()
		if err != nil {
			http.Error(
				w,
				`{"error":"Could not load messages"}`,
				http.StatusInternalServerError,
			)
			return
		}

		if msgs == nil {
			msgs = []string{}
		}

		if err := json.NewEncoder(w).Encode(
			map[string]interface{}{"messages": msgs},
		); err != nil {
			log.Printf("Could not encode messages: %v", err)
		}

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 8192)

		var input struct {
			Name string `json:"name"`
			Text string `json:"text"`
		}

		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(
				w,
				`{"error":"Invalid JSON"}`,
				http.StatusBadRequest,
			)
			return
		}

		name := strings.TrimSpace(input.Name)
		message := strings.TrimSpace(input.Text)

		if name == "" {
			name = "guest"
		}

		if len(name) > 50 || message == "" || len(message) > 1000 {
			http.Error(
				w,
				`{"error":"Name or message length is invalid"}`,
				http.StatusBadRequest,
			)
			return
		}

		if err := saveMessage(name, message); err != nil {
			http.Error(
				w,
				`{"error":"Could not save message"}`,
				http.StatusInternalServerError,
			)
			return
		}

		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(
			map[string]bool{"ok": true},
		); err != nil {
			log.Printf("Could not encode response: %v", err)
		}

	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(
			w,
			`{"error":"Method not allowed"}`,
			http.StatusMethodNotAllowed,
		)
	}
}

func initialModel(user string) model {
	ti := textinput.New()
	ti.Placeholder = "Write a message and press Enter..."
	ti.Focus()
	ti.CharLimit = 120
	ti.Width = 50

	msgs, err := loadMessages()

	return model{
		username:  user,
		messages:  msgs,
		textInput: ti,
		err:       err,
	}
}

func teaHandler(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
	user := sess.User()
	if user == "" {
		user = "guest"
	}

	return initialModel(user), []tea.ProgramOption{
		tea.WithAltScreen(),
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit

		case "enter":
			value := strings.TrimSpace(m.textInput.Value())
			if value == "" {
				return m, nil
			}

			if err := saveMessage(m.username, value); err != nil {
				m.err = err
				return m, nil
			}

			m.submitted = true
			return m, tea.Quit
		}
	}

	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m model) View() string {
	if m.submitted {
		return successStyle.Render(
			"Thanks for signing the guestbook! Goodbye!\n",
		)
	}

	header := titleStyle.Render("SSH Guestbook") + " " +
		userTagStyle.Render("(@"+m.username+")")

	var b strings.Builder

	if m.err != nil {
		b.WriteString("Error: " + m.err.Error() + "\n")
	}

	if len(m.messages) == 0 {
		b.WriteString(timestampStyle.Render(
			"No messages yet. Be the first to leave one!\n",
		))
	} else {
		start := 0
		if len(m.messages) > 8 {
			start = len(m.messages) - 8
		}

		for _, line := range m.messages[start:] {
			if strings.HasPrefix(line, "[") &&
				strings.Contains(line, "] ") {
				parts := strings.SplitN(line, "] ", 2)
				timePart := timestampStyle.Render(parts[0] + "]")
				b.WriteString(fmt.Sprintf("%s %s\n", timePart, parts[1]))
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	inputView := inputContainerStyle.Render(m.textInput.View())
	footer := footerStyle.Render(
		"Press Enter to submit • Esc/Ctrl+C to quit",
	)

	content := fmt.Sprintf(
		"%s\n\n%s\n%s\n%s",
		header,
		b.String(),
		inputView,
		footer,
	)

	return boxStyle.Render(content)
}

func main() {
	s, err := wish.NewServer(
		wish.WithAddress("127.0.0.1:2222"),
		wish.WithHostKeyPath(".ssh/id_ed25519"),
		wish.WithMiddleware(
			wishtea.Middleware(teaHandler),
			logging.Middleware(),
		),
	)
	if err != nil {
		log.Fatalf("could not create server: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/messages", messagesAPIHandler)
	mux.Handle("/", http.FileServer(http.Dir("web")))

	go func() {
		log.Println("Browser website: http://127.0.0.1:8080")
		if err := http.ListenAndServe("127.0.0.1:8080", mux); err != nil {
			log.Printf("HTTP server stopped: %v", err)
		}
	}()

	log.Println("Styled Guestbook server listening on 127.0.0.1:2222")

	if err := s.ListenAndServe(); err != nil &&
		!errors.Is(err, net.ErrClosed) {
		log.Fatalf("server error: %v", err)
	}
}
