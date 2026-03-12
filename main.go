// Messaging Service - Handles message sending, storage, and delivery
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/IBM/sarama"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/jwtauth/v5"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	DefaultPort   = "8083"
	CassandraHost = "cassandra:9042"
	RedisAddr     = "redis:6379"
	KafkaBrokers  = "kafka:9092"
	TokenSecret   = "your-secret-key-here"
)

// Server holds all dependencies
type Server struct {
	cassandra   *gocql.Session
	redis       *redis.Client
	kafka       sarama.SyncProducer
	router      *chi.Mux
	tokenAuth   *jwtauth.JWTAuth
}

// Message represents a chat message
type Message struct {
	MessageID        gocql.UUID `json:"message_id"`
	ChatID           gocql.UUID `json:"chat_id"`
	SenderID         gocql.UUID `json:"sender_id"`
	MessageType      string     `json:"message_type"`
	Content          string     `json:"content"`
	EncryptedPayload []byte     `json:"encrypted_payload,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	EditedAt         *time.Time `json:"edited_at,omitempty"`
	IsDeleted        bool       `json:"is_deleted"`
	ReplyTo          *gocql.UUID `json:"reply_to,omitempty"`
}

// Chat represents a conversation
type Chat struct {
	ChatID      gocql.UUID `json:"chat_id"`
	ChatType    string     `json:"chat_type"`
	Title       string     `json:"title"`
	CreatedBy   gocql.UUID `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Participants []ChatParticipant `json:"participants,omitempty"`
}

// ChatParticipant represents a member of a chat
type ChatParticipant struct {
	UserID             gocql.UUID `json:"user_id"`
	Role               string     `json:"role"`
	JoinedAt           time.Time  `json:"joined_at"`
	LastReadMessageID  *gocql.UUID `json:"last_read_message_id,omitempty"`
}

// SendMessageRequest represents a request to send a message
type SendMessageRequest struct {
	ChatID           string `json:"chat_id"`
	MessageType      string `json:"message_type"`
	Content          string `json:"content"`
	EncryptedPayload []byte `json:"encrypted_payload"`
	ReplyTo          string `json:"reply_to,omitempty"`
}

// MessageEvent represents a message event for Kafka
type MessageEvent struct {
	EventType   string    `json:"event_type"`
	MessageID   string    `json:"message_id"`
	ChatID      string    `json:"chat_id"`
	SenderID    string    `json:"sender_id"`
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
	Recipients  []string  `json:"recipients"`
}

func main() {
	// Initialize Cassandra
	cluster := gocql.NewCluster(getEnv("CASSANDRA_HOST", CassandraHost))
	cluster.Keyspace = "messaging"
	cluster.Consistency = gocql.Quorum
	cluster.Timeout = 5 * time.Second
	
	session, err := cluster.CreateSession()
	if err != nil {
		log.Fatalf("Failed to connect to Cassandra: %v", err)
	}
	defer session.Close()
	
	// Initialize Redis
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{
		Addr: getEnv("REDIS_ADDR", RedisAddr),
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer rdb.Close()
	
	// Initialize Kafka producer
	kafkaConfig := sarama.NewConfig()
	kafkaConfig.Producer.RequiredAcks = sarama.WaitForAll
	kafkaConfig.Producer.Retry.Max = 3
	kafkaConfig.Producer.Return.Successes = true
	
	producer, err := sarama.NewSyncProducer([]string{getEnv("KAFKA_BROKERS", KafkaBrokers)}, kafkaConfig)
	if err != nil {
		log.Fatalf("Failed to create Kafka producer: %v", err)
	}
	defer producer.Close()
	
	// Initialize server
	srv := &Server{
		cassandra: session,
		redis:     rdb,
		kafka:     producer,
		router:    chi.NewRouter(),
		tokenAuth: jwtauth.New("HS256", []byte(getEnv("JWT_SECRET", TokenSecret)), nil),
	}
	
	srv.setupRoutes()
	
	port := getEnv("PORT", DefaultPort)
	log.Printf("Messaging Service starting on port %s", port)
	if err := http.ListenAndServe(":"+port, srv.router); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func (s *Server) setupRoutes() {
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(middleware.Timeout(30 * time.Second))
	
	// Health check
	s.router.Get("/health", s.healthHandler)
	
	// Protected routes
	s.router.Route("/api/v1", func(r chi.Router) {
		r.Use(jwtauth.Verifier(s.tokenAuth))
		r.Use(jwtauth.Authenticator(s.tokenAuth))
		
		// Chat routes
		r.Post("/chats", s.createChat)
		r.Get("/chats", s.getChats)
		r.Get("/chats/{chatID}", s.getChat)
		r.Put("/chats/{chatID}", s.updateChat)
		r.Delete("/chats/{chatID}", s.deleteChat)
		
		// Chat participants
		r.Post("/chats/{chatID}/participants", s.addParticipant)
		r.Delete("/chats/{chatID}/participants/{userID}", s.removeParticipant)
		r.Put("/chats/{chatID}/participants/{userID}/role", s.updateParticipantRole)
		
		// Message routes
		r.Post("/chats/{chatID}/messages", s.sendMessage)
		r.Get("/chats/{chatID}/messages", s.getMessages)
		r.Get("/chats/{chatID}/messages/{messageID}", s.getMessage)
		r.Put("/chats/{chatID}/messages/{messageID}", s.editMessage)
		r.Delete("/chats/{chatID}/messages/{messageID}", s.deleteMessage)
		
		// Message status
		r.Post("/chats/{chatID}/messages/{messageID}/read", s.markAsRead)
		r.Get("/chats/{chatID}/messages/{messageID}/status", s.getMessageStatus)
		
		// Typing indicators
		r.Post("/chats/{chatID}/typing", s.sendTypingIndicator)
	})
}

// Health check handler
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	
	// Check Cassandra
	if err := s.cassandra.Query("SELECT now() FROM system.local").Exec(); err != nil {
		http.Error(w, `{"status":"unhealthy","error":"cassandra"}`, http.StatusServiceUnavailable)
		return
	}
	
	// Check Redis
	if err := s.redis.Ping(ctx).Err(); err != nil {
		http.Error(w, `{"status":"unhealthy","error":"redis"}`, http.StatusServiceUnavailable)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

// Create a new chat
func (s *Server) createChat(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	userID, _ := uuid.Parse(claims["user_id"].(string))
	
	var req struct {
		ChatType string   `json:"chat_type"`
		Title    string   `json:"title"`
		Members  []string `json:"members"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	
	chatID := gocql.TimeUUID()
	now := time.Now()
	
	// Insert chat
	query := `INSERT INTO chats (chat_id, chat_type, title, created_by, created_at, updated_at) 
	          VALUES (?, ?, ?, ?, ?, ?)`
	if err := s.cassandra.Query(query, chatID, req.ChatType, req.Title, userID, now, now).Exec(); err != nil {
		http.Error(w, `{"error":"failed to create chat"}`, http.StatusInternalServerError)
		return
	}
	
	// Add creator as admin
	participantQuery := `INSERT INTO chat_participants (chat_id, user_id, role, joined_at) VALUES (?, ?, 'admin', ?)`
	if err := s.cassandra.Query(participantQuery, chatID, userID, now).Exec(); err != nil {
		http.Error(w, `{"error":"failed to add participant"}`, http.StatusInternalServerError)
		return
	}
	
	// Add other members
	for _, memberIDStr := range req.Members {
		memberID, err := uuid.Parse(memberIDStr)
		if err != nil {
			continue
		}
		s.cassandra.Query(participantQuery, chatID, memberID, now).Exec()
	}
	
	chat := Chat{
		ChatID:    chatID,
		ChatType:  req.ChatType,
		Title:     req.Title,
		CreatedBy: gocql.UUID(userID),
		CreatedAt: now,
		UpdatedAt: now,
	}
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(chat)
}

// Get user's chats
func (s *Server) getChats(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	userID, _ := uuid.Parse(claims["user_id"].(string))
	
	// Get chat IDs where user is a participant
	var chatIDs []gocql.UUID
	iter := s.cassandra.Query(
		`SELECT chat_id FROM chat_participants WHERE user_id = ?`,
		userID,
	).Iter()
	
	var chatID gocql.UUID
	for iter.Scan(&chatID) {
		chatIDs = append(chatIDs, chatID)
	}
	iter.Close()
	
	// Fetch chat details
	var chats []Chat
	for _, cid := range chatIDs {
		var chat Chat
		err := s.cassandra.Query(
			`SELECT chat_id, chat_type, title, created_by, created_at, updated_at FROM chats WHERE chat_id = ?`,
			cid,
		).Scan(&chat.ChatID, &chat.ChatType, &chat.Title, &chat.CreatedBy, &chat.CreatedAt, &chat.UpdatedAt)
		
		if err == nil {
			chats = append(chats, chat)
		}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"chats": chats,
		"count": len(chats),
	})
}

// Get chat details
func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	var chat Chat
	err = s.cassandra.Query(
		`SELECT chat_id, chat_type, title, created_by, created_at, updated_at FROM chats WHERE chat_id = ?`,
		chatID,
	).Scan(&chat.ChatID, &chat.ChatType, &chat.Title, &chat.CreatedBy, &chat.CreatedAt, &chat.UpdatedAt)
	
	if err != nil {
		http.Error(w, `{"error":"chat not found"}`, http.StatusNotFound)
		return
	}
	
	// Get participants
	var participants []ChatParticipant
	piter := s.cassandra.Query(
		`SELECT user_id, role, joined_at, last_read_message_id FROM chat_participants WHERE chat_id = ?`,
		chatID,
	).Iter()
	
	var p ChatParticipant
	for piter.Scan(&p.UserID, &p.Role, &p.JoinedAt, &p.LastReadMessageID) {
		participants = append(participants, p)
	}
	piter.Close()
	chat.Participants = participants
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chat)
}

// Update chat
func (s *Server) updateChat(w http.ResponseWriter, r *http.Request) {
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	
	query := `UPDATE chats SET title = ?, updated_at = ? WHERE chat_id = ?`
	if err := s.cassandra.Query(query, req.Title, time.Now(), chatID).Exec(); err != nil {
		http.Error(w, `{"error":"failed to update chat"}`, http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// Delete chat (soft delete)
func (s *Server) deleteChat(w http.ResponseWriter, r *http.Request) {
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	query := `UPDATE chats SET chat_type = 'deleted', updated_at = ? WHERE chat_id = ?`
	if err := s.cassandra.Query(query, time.Now(), chatID).Exec(); err != nil {
		http.Error(w, `{"error":"failed to delete chat"}`, http.StatusInternalServerError)
		return
	}
	
	w.WriteHeader(http.StatusNoContent)
}

// Add participant to chat
func (s *Server) addParticipant(w http.ResponseWriter, r *http.Request) {
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	var req struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		http.Error(w, `{"error":"invalid user ID"}`, http.StatusBadRequest)
		return
	}
	
	if req.Role == "" {
		req.Role = "member"
	}
	
	query := `INSERT INTO chat_participants (chat_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)`
	if err := s.cassandra.Query(query, chatID, userID, req.Role, time.Now()).Exec(); err != nil {
		http.Error(w, `{"error":"failed to add participant"}`, http.StatusInternalServerError)
		return
	}
	
	w.WriteHeader(http.StatusCreated)
}

// Remove participant from chat
func (s *Server) removeParticipant(w http.ResponseWriter, r *http.Request) {
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	userIDStr := chi.URLParam(r, "userID")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid user ID"}`, http.StatusBadRequest)
		return
	}
	
	query := `DELETE FROM chat_participants WHERE chat_id = ? AND user_id = ?`
	if err := s.cassandra.Query(query, chatID, userID).Exec(); err != nil {
		http.Error(w, `{"error":"failed to remove participant"}`, http.StatusInternalServerError)
		return
	}
	
	w.WriteHeader(http.StatusNoContent)
}

// Update participant role
func (s *Server) updateParticipantRole(w http.ResponseWriter, r *http.Request) {
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	userIDStr := chi.URLParam(r, "userID")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid user ID"}`, http.StatusBadRequest)
		return
	}
	
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	
	query := `UPDATE chat_participants SET role = ? WHERE chat_id = ? AND user_id = ?`
	if err := s.cassandra.Query(query, req.Role, chatID, userID).Exec(); err != nil {
		http.Error(w, `{"error":"failed to update role"}`, http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// Send message
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	senderID, _ := uuid.Parse(claims["user_id"].(string))
	
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	
	// Verify user is participant
	var count int
	if err := s.cassandra.Query(
		`SELECT COUNT(*) FROM chat_participants WHERE chat_id = ? AND user_id = ?`,
		chatID, senderID,
	).Scan(&count); err != nil || count == 0 {
		http.Error(w, `{"error":"not a participant"}`, http.StatusForbidden)
		return
	}
	
	// Create message
	messageID := gocql.TimeUUID()
	now := time.Now()
	
	var replyTo *gocql.UUID
	if req.ReplyTo != "" {
		replyUUID, err := gocql.ParseUUID(req.ReplyTo)
		if err == nil {
			replyTo = &replyUUID
		}
	}
	
	// Insert message
	query := `INSERT INTO messages_by_chat (chat_id, message_id, sender_id, message_type, content, encrypted_payload, created_at, is_deleted, reply_to)
	          VALUES (?, ?, ?, ?, ?, ?, ?, false, ?)`
	if err := s.cassandra.Query(query, chatID, messageID, senderID, req.MessageType, req.Content, 
		req.EncryptedPayload, now, replyTo).Exec(); err != nil {
		http.Error(w, `{"error":"failed to send message"}`, http.StatusInternalServerError)
		return
	}
	
	// Update chat timestamp
	s.cassandra.Query(`UPDATE chats SET updated_at = ? WHERE chat_id = ?`, now, chatID).Exec()
	
	// Get recipients
	var recipients []string
	iter := s.cassandra.Query(
		`SELECT user_id FROM chat_participants WHERE chat_id = ?`,
		chatID,
	).Iter()
	
	var recipientID gocql.UUID
	for iter.Scan(&recipientID) {
		if recipientID.String() != senderID.String() {
			recipients = append(recipients, recipientID.String())
		}
	}
	iter.Close()
	
	// Publish to Kafka for delivery
	event := MessageEvent{
		EventType:  "new_message",
		MessageID:  messageID.String(),
		ChatID:     chatID.String(),
		SenderID:   senderID.String(),
		Content:    req.Content,
		Timestamp:  now,
		Recipients: recipients,
	}
	
	eventJSON, _ := json.Marshal(event)
	msg := &sarama.ProducerMessage{
		Topic: "message-events",
		Key:   sarama.StringEncoder(chatID.String()),
		Value: sarama.ByteEncoder(eventJSON),
	}
	
	_, _, err = s.kafka.SendMessage(msg)
	if err != nil {
		log.Printf("Failed to publish to Kafka: %v", err)
	}
	
	// Publish for push notifications
	notifEvent := map[string]interface{}{
		"type":       "new_message",
		"chat_id":    chatID.String(),
		"sender_id":  senderID.String(),
		"recipients": recipients,
	}
	notifJSON, _ := json.Marshal(notifEvent)
	notifMsg := &sarama.ProducerMessage{
		Topic: "notification-events",
		Value: sarama.ByteEncoder(notifJSON),
	}
	s.kafka.SendMessage(notifMsg)
	
	message := Message{
		MessageID:        messageID,
		ChatID:           chatID,
		SenderID:         gocql.UUID(senderID),
		MessageType:      req.MessageType,
		Content:          req.Content,
		EncryptedPayload: req.EncryptedPayload,
		CreatedAt:        now,
		ReplyTo:          replyTo,
	}
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(message)
}

// Get messages in chat
func (s *Server) getMessages(w http.ResponseWriter, r *http.Request) {
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	// Parse pagination params
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
		if limit > 100 {
			limit = 100
		}
	}
	
	before := r.URL.Query().Get("before")
	
	var query string
	var iter *gocql.Iter
	
	if before != "" {
		beforeUUID, err := gocql.ParseUUID(before)
		if err != nil {
			http.Error(w, `{"error":"invalid before parameter"}`, http.StatusBadRequest)
			return
		}
		query = `SELECT message_id, sender_id, message_type, content, encrypted_payload, created_at, edited_at, is_deleted, reply_to 
		         FROM messages_by_chat WHERE chat_id = ? AND message_id < ? ORDER BY message_id DESC LIMIT ?`
		iter = s.cassandra.Query(query, chatID, beforeUUID, limit).Iter()
	} else {
		query = `SELECT message_id, sender_id, message_type, content, encrypted_payload, created_at, edited_at, is_deleted, reply_to 
		         FROM messages_by_chat WHERE chat_id = ? ORDER BY message_id DESC LIMIT ?`
		iter = s.cassandra.Query(query, chatID, limit).Iter()
	}
	
	var messages []Message
	var m Message
	for iter.Scan(&m.MessageID, &m.SenderID, &m.MessageType, &m.Content, &m.EncryptedPayload,
		&m.CreatedAt, &m.EditedAt, &m.IsDeleted, &m.ReplyTo) {
		messages = append(messages, m)
	}
	iter.Close()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"messages": messages,
		"count":    len(messages),
		"chat_id":  chatID,
	})
}

// Get single message
func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) {
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	messageIDStr := chi.URLParam(r, "messageID")
	messageID, err := gocql.ParseUUID(messageIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid message ID"}`, http.StatusBadRequest)
		return
	}
	
	var message Message
	query := `SELECT message_id, sender_id, message_type, content, encrypted_payload, created_at, edited_at, is_deleted, reply_to 
	          FROM messages_by_chat WHERE chat_id = ? AND message_id = ?`
	err = s.cassandra.Query(query, chatID, messageID).Scan(
		&message.MessageID, &message.SenderID, &message.MessageType, &message.Content,
		&message.EncryptedPayload, &message.CreatedAt, &message.EditedAt, &message.IsDeleted, &message.ReplyTo,
	)
	
	if err != nil {
		http.Error(w, `{"error":"message not found"}`, http.StatusNotFound)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(message)
}

// Edit message
func (s *Server) editMessage(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	senderID, _ := uuid.Parse(claims["user_id"].(string))
	
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	messageIDStr := chi.URLParam(r, "messageID")
	messageID, err := gocql.ParseUUID(messageIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid message ID"}`, http.StatusBadRequest)
		return
	}
	
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	
	now := time.Now()
	query := `UPDATE messages_by_chat SET content = ?, edited_at = ? WHERE chat_id = ? AND message_id = ?`
	if err := s.cassandra.Query(query, req.Content, now, chatID, messageID).Exec(); err != nil {
		http.Error(w, `{"error":"failed to edit message"}`, http.StatusInternalServerError)
		return
	}
	
	// Publish edit event
	event := map[string]interface{}{
		"type":       "message_edited",
		"chat_id":    chatID.String(),
		"message_id": messageID.String(),
		"content":    req.Content,
	}
	eventJSON, _ := json.Marshal(event)
	msg := &sarama.ProducerMessage{
		Topic: "message-events",
		Value: sarama.ByteEncoder(eventJSON),
	}
	s.kafka.SendMessage(msg)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "edited"})
}

// Delete message (soft delete)
func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	messageIDStr := chi.URLParam(r, "messageID")
	messageID, err := gocql.ParseUUID(messageIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid message ID"}`, http.StatusBadRequest)
		return
	}
	
	query := `UPDATE messages_by_chat SET is_deleted = true WHERE chat_id = ? AND message_id = ?`
	if err := s.cassandra.Query(query, chatID, messageID).Exec(); err != nil {
		http.Error(w, `{"error":"failed to delete message"}`, http.StatusInternalServerError)
		return
	}
	
	// Publish delete event
	event := map[string]interface{}{
		"type":       "message_deleted",
		"chat_id":    chatID.String(),
		"message_id": messageID.String(),
	}
	eventJSON, _ := json.Marshal(event)
	msg := &sarama.ProducerMessage{
		Topic: "message-events",
		Value: sarama.ByteEncoder(eventJSON),
	}
	s.kafka.SendMessage(msg)
	
	w.WriteHeader(http.StatusNoContent)
}

// Mark message as read
func (s *Server) markAsRead(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	userID, _ := uuid.Parse(claims["user_id"].(string))
	
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	messageIDStr := chi.URLParam(r, "messageID")
	messageID, err := gocql.ParseUUID(messageIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid message ID"}`, http.StatusBadRequest)
		return
	}
	
	now := time.Now()
	query := `INSERT INTO message_status (message_id, user_id, status, updated_at) VALUES (?, ?, 'read', ?)`
	if err := s.cassandra.Query(query, messageID, userID, now).Exec(); err != nil {
		http.Error(w, `{"error":"failed to mark as read"}`, http.StatusInternalServerError)
		return
	}
	
	// Update last read message for participant
	s.cassandra.Query(
		`UPDATE chat_participants SET last_read_message_id = ? WHERE chat_id = ? AND user_id = ?`,
		messageID, chatID, userID,
	).Exec()
	
	// Publish read receipt event
	event := map[string]interface{}{
		"type":       "message_read",
		"chat_id":    chatID.String(),
		"message_id": messageID.String(),
		"user_id":    userID.String(),
	}
	eventJSON, _ := json.Marshal(event)
	msg := &sarama.ProducerMessage{
		Topic: "message-events",
		Value: sarama.ByteEncoder(eventJSON),
	}
	s.kafka.SendMessage(msg)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "read"})
}

// Get message status (delivered/read by whom)
func (s *Server) getMessageStatus(w http.ResponseWriter, r *http.Request) {
	messageIDStr := chi.URLParam(r, "messageID")
	messageID, err := gocql.ParseUUID(messageIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid message ID"}`, http.StatusBadRequest)
		return
	}
	
	var statuses []struct {
		UserID    gocql.UUID `json:"user_id"`
		Status    string     `json:"status"`
		UpdatedAt time.Time  `json:"updated_at"`
	}
	
	iter := s.cassandra.Query(
		`SELECT user_id, status, updated_at FROM message_status WHERE message_id = ?`,
		messageID,
	).Iter()
	
	var s struct {
		UserID    gocql.UUID
		Status    string
		UpdatedAt time.Time
	}
	for iter.Scan(&s.UserID, &s.Status, &s.UpdatedAt) {
		statuses = append(statuses, s)
	}
	iter.Close()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message_id": messageID,
		"statuses":   statuses,
	})
}

// Send typing indicator
func (s *Server) sendTypingIndicator(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	userID, _ := uuid.Parse(claims["user_id"].(string))
	
	chatIDStr := chi.URLParam(r, "chatID")
	chatID, err := gocql.ParseUUID(chatIDStr)
	if err != nil {
		http.Error(w, `{"error":"invalid chat ID"}`, http.StatusBadRequest)
		return
	}
	
	var req struct {
		Action string `json:"action"` // "typing" or "stopped"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Action = "typing"
	}
	
	// Publish typing event
	event := map[string]interface{}{
		"type":    "typing_indicator",
		"chat_id": chatID.String(),
		"user_id": userID.String(),
		"action":  req.Action,
	}
	eventJSON, _ := json.Marshal(event)
	msg := &sarama.ProducerMessage{
		Topic: "presence-events",
		Value: sarama.ByteEncoder(eventJSON),
	}
	s.kafka.SendMessage(msg)
	
	w.WriteHeader(http.StatusNoContent)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
