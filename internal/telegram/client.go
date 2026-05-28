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
	"sync"
	"time"
)

const (
	_pollTimeout   = 30 * time.Second
	_retryDelay    = 3 * time.Second
	_clientTimeout = 60 * time.Second
)

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

// BotClient is a Telegram Bot API client using long polling.
type BotClient struct {
	token   string
	baseURL string
	http    *http.Client
	offset  int

	mu       sync.Mutex
	messages []Message
	onMsg    MessageHandler

	allowedUserIDs map[int64]bool
	botUserID      int64

	onPollMessage func(Message)

	// cache for getChatMember results: key = chatID, value = expiry time
	memberCacheMu sync.Mutex
	memberCache   map[int64]memberCacheEntry
}

type memberCacheEntry struct {
	allowed bool
	expiry  time.Time
}

// NewBotClient creates a new Telegram client.
func NewBotClient(token string, allowedUserIDs []int64) *BotClient {
	allowed := make(map[int64]bool, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		allowed[id] = true
	}
	if len(allowed) > 0 {
		log.Printf("[telegram] user whitelist enabled: %v", allowed)
	}

	return &BotClient{
		token:          token,
		baseURL:        fmt.Sprintf("https://api.telegram.org/bot%s", token),
		http:           &http.Client{Timeout: _clientTimeout},
		allowedUserIDs: allowed,
		memberCache:    make(map[int64]memberCacheEntry),
	}
}

// OnMessage registers a handler called for each incoming message.
func (c *BotClient) OnMessage(fn MessageHandler) {
	c.onMsg = fn
}

// BotUserID returns the bot's own Telegram user ID.
func (c *BotClient) BotUserID() int64 {
	return c.botUserID
}

// SetOnPollMessage registers a callback for dedup when a message is received from polling.
func (c *BotClient) SetOnPollMessage(fn func(Message)) {
	c.onPollMessage = fn
}

// FetchBotUserID calls getMe to learn this bot's numeric user ID.
func (c *BotClient) FetchBotUserID() error {
	url := fmt.Sprintf("%s/getMe", c.baseURL)
	resp, err := c.http.Get(url)
	if err != nil {
		return fmt.Errorf("getMe: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read getMe response: %w", err)
	}
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			ID int64 `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("unmarshal getMe: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("getMe returned ok=false: %s", body)
	}
	c.botUserID = result.Result.ID
	log.Printf("[telegram] bot user ID: %d", c.botUserID)
	return nil
}

// StartPolling blocks and polls Telegram for updates until ctx is cancelled.
func (c *BotClient) StartPolling(ctx context.Context) {
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

			// Filter: private chats by user whitelist; group chats by owner membership.
			if len(c.allowedUserIDs) > 0 {
				if msg.ChatID > 0 {
					// Private message — only allow whitelisted users.
					if !c.allowedUserIDs[msg.UserID] {
						log.Printf("[telegram] filtered private msg from user %d (%s)", msg.UserID, msg.Username)
						c.offset = u.UpdateID + 1
						continue
					}
				} else {
					// Group message — allow only if an allowed user is a member of this chat.
					if !c.isOwnerInChat(msg.ChatID) {
						log.Printf("[telegram] filtered group msg from chat %d (owner not a member)", msg.ChatID)
						c.offset = u.UpdateID + 1
						continue
					}
				}
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

			// Notify relay watcher for dedup.
			if c.onPollMessage != nil {
				c.onPollMessage(msg)
			}

			if c.onMsg != nil {
				c.onMsg(msg)
			}

			c.offset = u.UpdateID + 1
		}
	}
}

// DrainMessages returns all messages received within the last hour.
func (c *BotClient) DrainMessages() []Message {
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

// DispatchMessage adds a message to the buffer and triggers the onMsg callback.
// Used by the relay watcher to inject messages into the same pipeline as polling.
func (c *BotClient) DispatchMessage(msg Message) {
	c.mu.Lock()
	c.messages = append(c.messages, msg)
	c.mu.Unlock()

	if c.onMsg != nil {
		c.onMsg(msg)
	}
}

// SendMessage sends a text message to the given chat and returns the message ID.
func (c *BotClient) SendMessage(chatID int64, text string, replyTo int) (int, error) {
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
func (c *BotClient) SetReaction(chatID int64, messageID int, emoji string) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"reaction":   []map[string]string{{"type": "emoji", "emoji": emoji}},
	}
	return c.call("setMessageReaction", payload)
}

// EditMessage edits a previously sent text message.
func (c *BotClient) EditMessage(chatID int64, messageID int, text string) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
		"parse_mode": "HTML",
	}
	return c.call("editMessageText", payload)
}

// DownloadToTemp downloads a Telegram file to a temp file and returns the local path.
func (c *BotClient) DownloadToTemp(fileID string) (string, error) {
	return c.downloadToTemp(fileID, "tg_attachment_*")
}

// SendFile sends a file (photo or document) to the given chat using multipart upload.
func (c *BotClient) SendFile(chatID int64, filePath string, caption string, replyTo int) (int, error) {
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

	var sent struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(apiResp.Result, &sent); err == nil {
		return sent.MessageID, nil
	}
	return 0, nil
}

func (c *BotClient) downloadToTemp(fileID, pattern string) (string, error) {
	tgPath, err := c.getFilePath(fileID)
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

func (c *BotClient) getFilePath(fileID string) (string, error) {
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

func (c *BotClient) getUpdates(ctx context.Context) ([]update, error) {
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

func (c *BotClient) callWithID(method string, payload any) (int, error) {
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

func (c *BotClient) call(method string, payload any) error {
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

// isOwnerInChat checks (with 5-minute cache) whether any allowed user is a member of chatID.
func (c *BotClient) isOwnerInChat(chatID int64) bool {
	c.memberCacheMu.Lock()
	if entry, ok := c.memberCache[chatID]; ok && time.Now().Before(entry.expiry) {
		allowed := entry.allowed
		c.memberCacheMu.Unlock()
		return allowed
	}
	c.memberCacheMu.Unlock()

	allowed := false
	for ownerID := range c.allowedUserIDs {
		status, err := c.getChatMemberStatus(chatID, ownerID)
		if err != nil {
			log.Printf("[telegram] getChatMember error chat=%d user=%d: %v", chatID, ownerID, err)
			continue
		}
		if status == "member" || status == "administrator" || status == "creator" {
			allowed = true
			break
		}
	}

	c.memberCacheMu.Lock()
	c.memberCache[chatID] = memberCacheEntry{allowed: allowed, expiry: time.Now().Add(5 * time.Minute)}
	c.memberCacheMu.Unlock()
	return allowed
}

// getChatMemberStatus returns the membership status of userID in chatID.
func (c *BotClient) getChatMemberStatus(chatID, userID int64) (string, error) {
	url := fmt.Sprintf("%s/getChatMember?chat_id=%d&user_id=%d", c.baseURL, chatID, userID)
	resp, err := c.http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("getChatMember ok=false: %s", body)
	}
	return result.Result.Status, nil
}
