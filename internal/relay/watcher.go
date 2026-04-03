package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"cc-bot/internal/telegram"
)

// Watcher monitors the relay file written by the userbot and pushes
// new messages via the onMsg callback.
type Watcher struct {
	relayFile string
	onMsg     telegram.MessageHandler
	botUserID func() int64

	seenMu     sync.Mutex
	seenMsgIDs map[string]time.Time
}

// NewWatcher creates a relay file watcher.
func NewWatcher(relayFile string, botUserID func() int64, onMsg telegram.MessageHandler) *Watcher {
	return &Watcher{
		relayFile:  relayFile,
		onMsg:      onMsg,
		botUserID:  botUserID,
		seenMsgIDs: make(map[string]time.Time),
	}
}

// MarkSeen records a message ID in the dedup set (called by the poller for
// messages already seen via getUpdates).
func (w *Watcher) MarkSeen(msg telegram.Message) {
	key := fmt.Sprintf("%d:%d", msg.ChatID, msg.MessageID)
	w.seenMu.Lock()
	w.seenMsgIDs[key] = time.Now()
	w.seenMu.Unlock()
}

// Start begins watching the relay file until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	w.initSeenFromRelay()

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
			w.cleanupSeenIDs()

		case <-ticker.C:
			info, err := os.Stat(w.relayFile)
			if err != nil {
				continue
			}
			if !info.ModTime().After(lastModTime) {
				continue
			}
			lastModTime = info.ModTime()

			msgs := readFile(w.relayFile)
			for _, msg := range msgs {
				if !w.shouldPush(msg) {
					continue
				}

				if w.onMsg != nil {
					w.onMsg(msg)
				}

				log.Printf("[relay-watcher] pushed msg_id=%d chat=%d from=%s",
					msg.MessageID, msg.ChatID, msg.Username)
			}
		}
	}
}

func (w *Watcher) shouldPush(msg telegram.Message) bool {
	// Skip own messages (echo prevention).
	if botID := w.botUserID(); botID > 0 && msg.UserID == botID {
		return false
	}

	key := fmt.Sprintf("%d:%d", msg.ChatID, msg.MessageID)
	w.seenMu.Lock()
	_, seen := w.seenMsgIDs[key]
	if !seen {
		w.seenMsgIDs[key] = time.Now()
	}
	w.seenMu.Unlock()

	return !seen
}

func (w *Watcher) initSeenFromRelay() {
	msgs := readFile(w.relayFile)
	w.seenMu.Lock()
	defer w.seenMu.Unlock()
	for _, msg := range msgs {
		key := fmt.Sprintf("%d:%d", msg.ChatID, msg.MessageID)
		w.seenMsgIDs[key] = time.Now()
	}
	if len(msgs) > 0 {
		log.Printf("[relay-watcher] pre-loaded %d existing message IDs", len(msgs))
	}
}

func (w *Watcher) cleanupSeenIDs() {
	w.seenMu.Lock()
	defer w.seenMu.Unlock()
	cutoff := time.Now().Add(-2 * time.Hour)
	for key, t := range w.seenMsgIDs {
		if t.Before(cutoff) {
			delete(w.seenMsgIDs, key)
		}
	}
}

func readFile(path string) []telegram.Message {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var msgs []telegram.Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil
	}
	return msgs
}
