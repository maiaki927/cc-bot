package telegram

import "context"

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

// MessageHandler is called when a new message arrives.
type MessageHandler func(Message)

// Client defines the Telegram bot operations.
type Client interface {
	StartPolling(ctx context.Context)
	OnMessage(fn MessageHandler)
	DrainMessages() []Message
	SendMessage(chatID int64, text string, replyTo int) (int, error)
	SendFile(chatID int64, filePath string, caption string, replyTo int) (int, error)
	EditMessage(chatID int64, messageID int, text string) error
	SetReaction(chatID int64, messageID int, emoji string) error
	DownloadToTemp(fileID string) (string, error)
	BotUserID() int64
}
