package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	_pollTimeout   = 30 * time.Second
	_retryDelay    = 3 * time.Second
	_clientTimeout = 60 * time.Second
)

// Message is a simplified Telegram message for MCP consumers.
type Message struct {
	MessageID int    `json:"message_id"`
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	UserID    int64  `json:"user_id"`
	Date      int64  `json:"date"`
	// Attachment fields (optional).
	ImagePath        string `json:"image_path,omitempty"`
	AttachmentFileID string `json:"attachment_file_id,omitempty"`
}

// update is the Telegram Bot API update object.
type update struct {
	UpdateID int        `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int         `json:"message_id"`
	Chat      chat        `json:"chat"`
	From      *user       `json:"from"`
	Text      string      `json:"text"`
	Date      int64       `json:"date"`
	Photo     []photoSize `json:"photo"`
	Document  *document   `json:"document"`
	Caption   string      `json:"caption"`
}

type photoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int    `json:"file_size"`
}

type document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int    `json:"file_size"`
}

type chat struct {
	ID int64 `json:"id"`
}

type user struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type apiResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

// MessageHandler is called when a new message arrives.
type MessageHandler func(Message)

// Client is a Telegram Bot API client using long polling.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
	offset  int

	mu       sync.Mutex
	messages []Message
	onMsg    MessageHandler

	// Dedup: track seen message IDs to avoid duplicate push notifications.
	seenMu     sync.Mutex
	seenMsgIDs map[string]time.Time // key: "chatID:messageID" -> first seen time

	// Bot's own user ID (fetched via getMe on startup).
	botUserID int64

	allowedUserIDs map[int64]bool // if non-empty, only these user IDs are allowed
}

// NewClient creates a new Telegram client.
func NewClient(token string) *Client {
	c := &Client{
		token:          token,
		baseURL:        fmt.Sprintf("https://api.telegram.org/bot%s", token),
		http:           &http.Client{Timeout: _clientTimeout},
		seenMsgIDs:     make(map[string]time.Time),
		allowedUserIDs: make(map[int64]bool),
	}

	// Parse ALLOWED_USER_IDS env var (comma-separated list of Telegram user IDs).
	if raw := os.Getenv("ALLOWED_USER_IDS"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				log.Printf("[telegram] warning: invalid user ID in ALLOWED_USER_IDS: %q", s)
				continue
			}
			c.allowedUserIDs[id] = true
		}
		if len(c.allowedUserIDs) > 0 {
			log.Printf("[telegram] user whitelist enabled: %v", c.allowedUserIDs)
		}
	}

	return c
}

// OnMessage registers a handler called for each incoming message.
func (c *Client) OnMessage(fn MessageHandler) {
	c.onMsg = fn
}

// StartPolling blocks and polls Telegram for updates until ctx is cancelled.
func (c *Client) StartPolling(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, err := c.getUpdates(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(_retryDelay):
				continue
			}
		}

		for _, u := range updates {
			if u.Message == nil {
				c.offset = u.UpdateID + 1
				continue
			}

			m := u.Message
			hasContent := m.Text != "" || len(m.Photo) > 0 || m.Document != nil
			if !hasContent {
				c.offset = u.UpdateID + 1
				continue
			}

			msg := Message{
				MessageID: m.MessageID,
				ChatID:    m.Chat.ID,
				Text:      m.Text,
				Date:      m.Date,
			}
			if m.From != nil {
				msg.Username = m.From.Username
				msg.FirstName = m.From.FirstName
				msg.UserID = m.From.ID
			}

			// Filter by user whitelist if configured.
			if len(c.allowedUserIDs) > 0 && !c.allowedUserIDs[msg.UserID] {
				log.Printf("[telegram] filtered message from non-whitelisted user %d (%s) in chat %d",
					msg.UserID, msg.Username, msg.ChatID)
				c.offset = u.UpdateID + 1
				continue
			}

			// Use caption as text for photo/document messages.
			if msg.Text == "" && m.Caption != "" {
				msg.Text = m.Caption
			}

			// Handle photo attachments — pick largest size, download to temp file.
			if len(m.Photo) > 0 {
				best := m.Photo[len(m.Photo)-1]
				msg.AttachmentFileID = best.FileID
				if localPath, err := c.downloadToTemp(best.FileID, "photo_*.jpg"); err == nil {
					msg.ImagePath = localPath
				}
			}

			// Handle document attachments.
			if m.Document != nil {
				msg.AttachmentFileID = m.Document.FileID
			}

			c.mu.Lock()
			c.messages = append(c.messages, msg)
			c.mu.Unlock()

			// Record in dedup set so relay watcher skips this message.
			c.markSeen(msg)

			if c.onMsg != nil {
				c.onMsg(msg)
			}

			c.offset = u.UpdateID + 1
		}
	}
}

// DrainMessages returns all messages received within the last hour.
// This includes messages from getUpdates AND messages pushed by the relay watcher.
func (c *Client) DrainMessages() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().Unix() - 3600
	fresh := c.messages[:0]
	for _, m := range c.messages {
		if m.Date >= cutoff {
			fresh = append(fresh, m)
		}
	}
	c.messages = fresh

	result := make([]Message, len(c.messages))
	copy(result, c.messages)
	return result
}

var _relayFile = "/tmp/cc-bot-relay.json"
var _botName = "bot"

func init() {
	if p := os.Getenv("RELAY_FILE"); p != "" {
		_relayFile = p
	}
	if n := os.Getenv("BOT_NAME"); n != "" {
		_botName = n
	}
}

// fetchBotUserID calls getMe to learn this bot's numeric user ID for dedup.
func (c *Client) fetchBotUserID() {
	url := fmt.Sprintf("%s/getMe", c.baseURL)
	resp, err := c.http.Get(url)
	if err != nil {
		log.Printf("[telegram] getMe failed: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			ID int64 `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err == nil && result.OK {
		c.botUserID = result.Result.ID
		log.Printf("[telegram] bot user ID: %d", c.botUserID)
	}
}

// markSeen records a message ID in the dedup set.
func (c *Client) markSeen(msg Message) {
	key := fmt.Sprintf("%d:%d", msg.ChatID, msg.MessageID)
	c.seenMu.Lock()
	c.seenMsgIDs[key] = time.Now()
	c.seenMu.Unlock()
}

// shouldPushRelayMessage returns true if this relay message should be pushed.
func (c *Client) shouldPushRelayMessage(msg Message) bool {
	// Skip own messages (echo prevention).
	if c.botUserID > 0 && msg.UserID == c.botUserID {
		return false
	}

	// Skip already-seen messages (from getUpdates or previous relay reads).
	key := fmt.Sprintf("%d:%d", msg.ChatID, msg.MessageID)
	c.seenMu.Lock()
	_, seen := c.seenMsgIDs[key]
	if !seen {
		c.seenMsgIDs[key] = time.Now()
	}
	c.seenMu.Unlock()

	return !seen
}

// cleanupSeenIDs removes dedup entries older than 2 hours.
func (c *Client) cleanupSeenIDs() {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	cutoff := time.Now().Add(-2 * time.Hour)
	for key, t := range c.seenMsgIDs {
		if t.Before(cutoff) {
			delete(c.seenMsgIDs, key)
		}
	}
}

// initSeenFromRelay pre-loads existing relay messages into the dedup set
// to avoid re-pushing old messages after a bot restart.
func (c *Client) initSeenFromRelay() {
	msgs := readRelayFile()
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	for _, msg := range msgs {
		key := fmt.Sprintf("%d:%d", msg.ChatID, msg.MessageID)
		c.seenMsgIDs[key] = time.Now()
	}
	if len(msgs) > 0 {
		log.Printf("[relay-watcher] pre-loaded %d existing message IDs", len(msgs))
	}
}

// StartRelayWatcher monitors the relay file written by the userbot and pushes
// new messages to Claude Code via the onMsg callback.
func (c *Client) StartRelayWatcher(ctx context.Context) {
	c.fetchBotUserID()
	c.initSeenFromRelay()

	ticker := time.NewTicker(500 * time.Millisecond)
	cleanupTicker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	defer cleanupTicker.Stop()

	var lastModTime time.Time

	for {
		select {
		case <-ctx.Done():
			return

		case <-cleanupTicker.C:
			c.cleanupSeenIDs()

		case <-ticker.C:
			info, err := os.Stat(_relayFile)
			if err != nil {
				continue // file doesn't exist yet, userbot may not be running
			}
			if !info.ModTime().After(lastModTime) {
				continue // file hasn't changed
			}
			lastModTime = info.ModTime()

			msgs := readRelayFile()
			if len(msgs) == 0 {
				continue
			}

			for _, msg := range msgs {
				if !c.shouldPushRelayMessage(msg) {
					continue
				}

				c.mu.Lock()
				c.messages = append(c.messages, msg)
				c.mu.Unlock()

				if c.onMsg != nil {
					c.onMsg(msg)
				}

				log.Printf("[relay-watcher] pushed msg_id=%d chat=%d from=%s",
					msg.MessageID, msg.ChatID, msg.Username)
			}
		}
	}
}

func readRelayFile() []Message {
	data, err := os.ReadFile(_relayFile)
	if err != nil {
		return nil
	}
	var msgs []Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil
	}
	return msgs
}

// SendMessage sends a text message to the given chat and returns the message ID.
func (c *Client) SendMessage(chatID int64, text string, replyTo int) (int, error) {
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	if replyTo > 0 {
		payload["reply_parameters"] = map[string]any{
			"message_id": replyTo,
		}
	}
	return c.callWithID("sendMessage", payload)
}

// SetReaction sets an emoji reaction on a message.
func (c *Client) SetReaction(chatID int64, messageID int, emoji string) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"reaction":   []map[string]string{{"type": "emoji", "emoji": emoji}},
	}
	return c.call("setMessageReaction", payload)
}

// EditMessage edits a previously sent text message.
func (c *Client) EditMessage(chatID int64, messageID int, text string) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
		"parse_mode": "HTML",
	}
	return c.call("editMessageText", payload)
}

// GetFilePath returns the Telegram server file path for a given file_id.
func (c *Client) GetFilePath(fileID string) (string, error) {
	url := fmt.Sprintf("%s/getFile?file_id=%s", c.baseURL, fileID)
	resp, err := c.http.Get(url)
	if err != nil {
		return "", fmt.Errorf("getFile: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("getFile failed: %s", body)
	}
	return result.Result.FilePath, nil
}

// DownloadFile downloads a Telegram file to the given local destination path.
func (c *Client) DownloadFile(fileID, destPath string) error {
	tgPath, err := c.GetFilePath(fileID)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", c.token, tgPath)
	resp, err := c.http.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// DownloadToTemp downloads a Telegram file to a temp file and returns the local path.
func (c *Client) DownloadToTemp(fileID string) (string, error) {
	return c.downloadToTemp(fileID, "tg_attachment_*")
}

// downloadToTemp downloads a Telegram file to a temp file with a custom name pattern.
func (c *Client) downloadToTemp(fileID, pattern string) (string, error) {
	tgPath, err := c.GetFilePath(fileID)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", c.token, tgPath)
	resp, err := c.http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	tmpFile, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return "", fmt.Errorf("write temp: %w", err)
	}
	return tmpFile.Name(), nil
}

// SendFile sends a file (photo or document) to the given chat using multipart upload.
// Returns the message ID of the sent message.
func (c *Client) SendFile(chatID int64, filePath string, caption string, replyTo int) (int, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	_ = writer.WriteField("chat_id", fmt.Sprintf("%d", chatID))
	if caption != "" {
		_ = writer.WriteField("caption", caption)
		_ = writer.WriteField("parse_mode", "HTML")
	}
	if replyTo > 0 {
		_ = writer.WriteField("reply_parameters", fmt.Sprintf(`{"message_id":%d}`, replyTo))
	}

	ext := filepath.Ext(filePath)
	method := "sendDocument"
	fieldName := "document"
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp" {
		method = "sendPhoto"
		fieldName = "photo"
	}

	part, err := writer.CreateFormFile(fieldName, filepath.Base(filePath))
	if err != nil {
		return 0, fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return 0, fmt.Errorf("copy file: %w", err)
	}
	writer.Close()

	url := fmt.Sprintf("%s/%s", c.baseURL, method)
	resp, err := c.http.Post(url, writer.FormDataContentType(), body)
	if err != nil {
		return 0, fmt.Errorf("send file: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return 0, fmt.Errorf("unmarshal response: %w", err)
	}
	if !apiResp.OK {
		return 0, fmt.Errorf("telegram %s error: %s", method, respBody)
	}

	// Extract message_id from response.
	var sent struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(apiResp.Result, &sent); err == nil {
		return sent.MessageID, nil
	}
	return 0, nil
}

func (c *Client) getUpdates(ctx context.Context) ([]update, error) {
	url := fmt.Sprintf("%s/getUpdates?offset=%d&timeout=%d",
		c.baseURL, c.offset, int(_pollTimeout.Seconds()))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get updates: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var result struct {
		OK     bool     `json:"ok"`
		Result []update `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram API returned ok=false: %s", body)
	}

	return result.Result, nil
}

// callWithID is like call but also returns the message_id from the Telegram response.
func (c *Client) callWithID(method string, payload any) (int, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal payload: %w", err)
	}

	resp, err := c.http.Post(
		fmt.Sprintf("%s/%s", c.baseURL, method),
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return 0, fmt.Errorf("call %s: %w", method, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}

	var apiResp apiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return 0, fmt.Errorf("unmarshal response: %w", err)
	}
	if !apiResp.OK {
		return 0, fmt.Errorf("telegram %s error: %s", method, body)
	}

	var sent struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(apiResp.Result, &sent); err == nil {
		return sent.MessageID, nil
	}
	return 0, nil
}

func (c *Client) call(method string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	resp, err := c.http.Post(
		fmt.Sprintf("%s/%s", c.baseURL, method),
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return fmt.Errorf("call %s: %w", method, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var apiResp apiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}
	if !apiResp.OK {
		return fmt.Errorf("telegram %s error: %s", method, body)
	}

	return nil
}
