// WebSocket Gateway - Handles real-time connections and message delivery
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/jwtauth/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

const (
	DefaultPort  = "8084"
	RedisAddr    = "redis:6379"
	KafkaBrokers = "kafka:9092"
	TokenSecret  = "your-secret-key-here"
	
	// WebSocket settings
	WriteWait      = 10 * time.Second
	PongWait       = 60 * time.Second
	PingPeriod     = 54 * time.Second
	MaxMessageSize = 512 * 1024 // 512KB
)

// Server holds all dependencies
type Server struct {
	redis       *redis.Client
	kafka       sarama.Consumer
	kafkaProd   sarama.SyncProducer
	router      *chi.Mux
	tokenAuth   *jwtauth.JWTAuth
	hub         *Hub
}

// Hub maintains the set of active clients
type Hub struct {
	clients    map[string]*Client // userID -> Client
	register   chan *Client
	unregister chan *Client
	broadcast  chan *MessageEvent
	mu         sync.RWMutex
}

// Client is a WebSocket client
type Client struct {
	hub        *Hub
	conn       *websocket.Conn
	send       chan []byte
	userID     string
	deviceID   string
	serverNode string
}

// MessageEvent represents a message to be delivered
type MessageEvent struct {
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	UserID    string          `json:"user_id,omitempty"`
	ChatID    string          `json:"chat_id,omitempty"`
}

// WebSocketMessage represents incoming message from client
type WebSocketMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// OutgoingMessage represents outgoing message to client
type OutgoingMessage struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Payload   interface{} `json:"payload"`
}

// NewHub creates a new Hub
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[string]*Client),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan *MessageEvent, 256),
	}
}

// Run starts the hub
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client.userID] = client
			h.mu.Unlock()
			log.Printf("Client registered: %s", client.userID)
			
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client.userID]; ok {
				delete(h.clients, client.userID)
				close(client.send)
			}
			h.mu.Unlock()
			log.Printf("Client unregistered: %s", client.userID)
			
		case event := <-h.broadcast:
			h.mu.RLock()
			if client, ok := h.clients[event.UserID]; ok {
				select {
				case client.send <- event.Payload:
				default:
					// Client buffer full, close connection
					close(client.send)
					delete(h.clients, client.userID)
				}
			}
			h.mu.RUnlock()
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow all origins in development, configure for production
		return true
	},
}

func main() {
	ctx := context.Background()
	
	// Initialize Redis
	rdb := redis.NewClient(&redis.Options{
		Addr: getEnv("REDIS_ADDR", RedisAddr),
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer rdb.Close()
	
	// Initialize Kafka consumer
	kafkaConfig := sarama.NewConfig()
	kafkaConfig.Consumer.Return.Errors = true
	
	consumer, err := sarama.NewConsumer([]string{getEnv("KAFKA_BROKERS", KafkaBrokers)}, kafkaConfig)
	if err != nil {
		log.Fatalf("Failed to create Kafka consumer: %v", err)
	}
	defer consumer.Close()
	
	// Initialize Kafka producer
	kafkaProdConfig := sarama.NewConfig()
	kafkaProdConfig.Producer.RequiredAcks = sarama.WaitForAll
	kafkaProdConfig.Producer.Retry.Max = 3
	kafkaProdConfig.Producer.Return.Successes = true
	
	producer, err := sarama.NewSyncProducer([]string{getEnv("KAFKA_BROKERS", KafkaBrokers)}, kafkaProdConfig)
	if err != nil {
		log.Fatalf("Failed to create Kafka producer: %v", err)
	}
	defer producer.Close()
	
	// Initialize hub
	hub := NewHub()
	go hub.Run()
	
	// Initialize server
	srv := &Server{
		redis:     rdb,
		kafka:     consumer,
		kafkaProd: producer,
		router:    chi.NewRouter(),
		tokenAuth: jwtauth.New("HS256", []byte(getEnv("JWT_SECRET", TokenSecret)), nil),
		hub:       hub,
	}
	
	srv.setupRoutes()
	
	// Start Kafka consumers
	go srv.consumeMessageEvents()
	go srv.consumePresenceEvents()
	go srv.consumeNotificationEvents()
	
	port := getEnv("PORT", DefaultPort)
	log.Printf("WebSocket Gateway starting on port %s", port)
	if err := http.ListenAndServe(":"+port, srv.router); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func (s *Server) setupRoutes() {
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	
	// Health check
	s.router.Get("/health", s.healthHandler)
	
	// Metrics endpoint
	s.router.Get("/metrics", s.metricsHandler)
	
	// WebSocket endpoint
	s.router.Get("/ws", s.handleWebSocket)
	
	// REST API for presence
	s.router.Route("/api/v1", func(r chi.Router) {
		r.Use(jwtauth.Verifier(s.tokenAuth))
		r.Use(jwtauth.Authenticator(s.tokenAuth))
		
		r.Get("/presence/{userID}", s.getUserPresence)
		r.Post("/presence", s.updatePresence)
	})
}

// Health check handler
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	
	if err := s.redis.Ping(ctx).Err(); err != nil {
		http.Error(w, `{"status":"unhealthy"}`, http.StatusServiceUnavailable)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "healthy",
		"clients":   len(s.hub.clients),
		"node":      getEnv("NODE_ID", "node-1"),
	})
}

// Metrics handler
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	s.hub.mu.RLock()
	clientCount := len(s.hub.clients)
	s.hub.mu.RUnlock()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"connected_clients": clientCount,
		"server_node":       getEnv("NODE_ID", "node-1"),
		"timestamp":         time.Now().Unix(),
	})
}

// Handle WebSocket connection
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Get token from query parameter
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		http.Error(w, `{"error":"missing token"}`, http.StatusUnauthorized)
		return
	}
	
	// Validate token
	token, err := s.tokenAuth.Decode(tokenStr)
	if err != nil {
		http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
		return
	}
	
	claims, _ := token.AsMap(r.Context())
	userID := claims["user_id"].(string)
	deviceID := r.URL.Query().Get("device_id")
	if deviceID == "" {
		deviceID = uuid.New().String()
	}
	
	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}
	
	// Create client
	client := &Client{
		hub:        s.hub,
		conn:       conn,
		send:       make(chan []byte, 256),
		userID:     userID,
		deviceID:   deviceID,
		serverNode: getEnv("NODE_ID", "node-1"),
	}
	
	// Register client
	s.hub.register <- client
	
	// Update presence in Redis
	ctx := context.Background()
	presenceData := map[string]interface{}{
		"status":      "online",
		"last_seen":   time.Now().Unix(),
		"device_type": "web",
		"server_node": client.serverNode,
	}
	presenceJSON, _ := json.Marshal(presenceData)
	s.redis.Set(ctx, fmt.Sprintf("presence:%s", userID), presenceJSON, 5*time.Minute)
	
	// Publish presence update
	s.publishPresenceEvent(userID, "online")
	
	// Start goroutines for reading and writing
	go client.writePump()
	go client.readPump(s)
}

// Read pump handles incoming messages from client
func (c *Client) readPump(s *Server) {
	defer func() {
		s.hub.unregister <- c
		c.conn.Close()
		
		// Update presence to offline
		ctx := context.Background()
		presenceData := map[string]interface{}{
			"status":      "offline",
			"last_seen":   time.Now().Unix(),
			"device_type": "web",
		}
		presenceJSON, _ := json.Marshal(presenceData)
		s.redis.Set(ctx, fmt.Sprintf("presence:%s", c.userID), presenceJSON, 24*time.Hour)
		s.publishPresenceEvent(c.userID, "offline")
	}()
	
	c.conn.SetReadLimit(MaxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(PongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(PongWait))
		return nil
	})
	
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}
		
		// Parse incoming message
		var wsMsg WebSocketMessage
		if err := json.Unmarshal(message, &wsMsg); err != nil {
			log.Printf("Failed to parse message: %v", err)
			continue
		}
		
		// Handle different message types
		switch wsMsg.Type {
		case "message":
			s.handleIncomingMessage(c, wsMsg.Payload)
		case "typing":
			s.handleTypingIndicator(c, wsMsg.Payload)
		case "presence":
			s.handlePresenceUpdate(c, wsMsg.Payload)
		case "read_receipt":
			s.handleReadReceipt(c, wsMsg.Payload)
		case "ping":
			c.send <- []byte(`{"type":"pong","timestamp":` + fmt.Sprintf("%d", time.Now().Unix()) + `}`)
		default:
			log.Printf("Unknown message type: %s", wsMsg.Type)
		}
	}
}

// Write pump handles outgoing messages to client
func (c *Client) writePump() {
	ticker := time.NewTicker(PingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(WriteWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			
			c.conn.WriteMessage(websocket.TextMessage, message)
			
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(WriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Handle incoming message from client
func (s *Server) handleIncomingMessage(c *Client, payload json.RawMessage) {
	var msg struct {
		ChatID      string `json:"chat_id"`
		Content     string `json:"content"`
		MessageType string `json:"message_type"`
		ReplyTo     string `json:"reply_to,omitempty"`
	}
	
	if err := json.Unmarshal(payload, &msg); err != nil {
		log.Printf("Failed to parse message payload: %v", err)
		return
	}
	
	// Publish to Kafka for processing
	event := map[string]interface{}{
		"type":       "send_message",
		"user_id":    c.userID,
		"chat_id":    msg.ChatID,
		"content":    msg.Content,
		"message_type": msg.MessageType,
		"reply_to":   msg.ReplyTo,
		"timestamp":  time.Now().Unix(),
	}
	eventJSON, _ := json.Marshal(event)
	
	kafkaMsg := &sarama.ProducerMessage{
		Topic: "incoming-messages",
		Key:   sarama.StringEncoder(c.userID),
		Value: sarama.ByteEncoder(eventJSON),
	}
	
	_, _, err := s.kafkaProd.SendMessage(kafkaMsg)
	if err != nil {
		log.Printf("Failed to publish to Kafka: %v", err)
		// Send error to client
		errorMsg := OutgoingMessage{
			Type:      "error",
			Timestamp: time.Now(),
			Payload:   map[string]string{"error": "failed to send message"},
		}
		errorJSON, _ := json.Marshal(errorMsg)
		c.send <- errorJSON
	}
}

// Handle typing indicator
func (s *Server) handleTypingIndicator(c *Client, payload json.RawMessage) {
	var typing struct {
		ChatID string `json:"chat_id"`
		Action string `json:"action"`
	}
	
	if err := json.Unmarshal(payload, &typing); err != nil {
		return
	}
	
	event := map[string]interface{}{
		"type":    "typing_indicator",
		"chat_id": typing.ChatID,
		"user_id": c.userID,
		"action":  typing.Action,
	}
	eventJSON, _ := json.Marshal(event)
	
	kafkaMsg := &sarama.ProducerMessage{
		Topic: "presence-events",
		Value: sarama.ByteEncoder(eventJSON),
	}
	s.kafkaProd.SendMessage(kafkaMsg)
}

// Handle presence update
func (s *Server) handlePresenceUpdate(c *Client, payload json.RawMessage) {
	var presence struct {
		Status string `json:"status"`
	}
	
	if err := json.Unmarshal(payload, &presence); err != nil {
		return
	}
	
	ctx := context.Background()
	presenceData := map[string]interface{}{
		"status":      presence.Status,
		"last_seen":   time.Now().Unix(),
		"device_type": "web",
		"server_node": c.serverNode,
	}
	presenceJSON, _ := json.Marshal(presenceData)
	
	expiration := 5 * time.Minute
	if presence.Status == "offline" {
		expiration = 24 * time.Hour
	}
	
	s.redis.Set(ctx, fmt.Sprintf("presence:%s", c.userID), presenceJSON, expiration)
	s.publishPresenceEvent(c.userID, presence.Status)
}

// Handle read receipt
func (s *Server) handleReadReceipt(c *Client, payload json.RawMessage) {
	var receipt struct {
		ChatID    string `json:"chat_id"`
		MessageID string `json:"message_id"`
	}
	
	if err := json.Unmarshal(payload, &receipt); err != nil {
		return
	}
	
	event := map[string]interface{}{
		"type":       "read_receipt",
		"chat_id":    receipt.ChatID,
		"message_id": receipt.MessageID,
		"user_id":    c.userID,
		"timestamp":  time.Now().Unix(),
	}
	eventJSON, _ := json.Marshal(event)
	
	kafkaMsg := &sarama.ProducerMessage{
		Topic: "message-events",
		Value: sarama.ByteEncoder(eventJSON),
	}
	s.kafkaProd.SendMessage(kafkaMsg)
}

// Publish presence event to Kafka
func (s *Server) publishPresenceEvent(userID, status string) {
	event := map[string]interface{}{
		"type":      "presence_update",
		"user_id":   userID,
		"status":    status,
		"timestamp": time.Now().Unix(),
	}
	eventJSON, _ := json.Marshal(event)
	
	kafkaMsg := &sarama.ProducerMessage{
		Topic: "presence-events",
		Value: sarama.ByteEncoder(eventJSON),
	}
	s.kafkaProd.SendMessage(kafkaMsg)
}

// Consume message events from Kafka
func (s *Server) consumeMessageEvents() {
	partitionConsumer, err := s.kafka.ConsumePartition("message-events", 0, sarama.OffsetNewest)
	if err != nil {
		log.Printf("Failed to consume message events: %v", err)
		return
	}
	defer partitionConsumer.Close()
	
	for msg := range partitionConsumer.Messages() {
		var event MessageEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			log.Printf("Failed to parse message event: %v", err)
			continue
		}
		
		// Deliver to recipient if online
		s.hub.mu.RLock()
		if client, ok := s.hub.clients[event.UserID]; ok {
			outgoing := OutgoingMessage{
				Type:      event.EventType,
				Timestamp: time.Now(),
				Payload:   event.Payload,
			}
			outgoingJSON, _ := json.Marshal(outgoing)
			select {
			case client.send <- outgoingJSON:
			default:
				// Client buffer full
			}
		}
		s.hub.mu.RUnlock()
	}
}

// Consume presence events from Kafka
func (s *Server) consumePresenceEvents() {
	partitionConsumer, err := s.kafka.ConsumePartition("presence-events", 0, sarama.OffsetNewest)
	if err != nil {
		log.Printf("Failed to consume presence events: %v", err)
		return
	}
	defer partitionConsumer.Close()
	
	for msg := range partitionConsumer.Messages() {
		var event map[string]interface{}
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			continue
		}
		
		eventType, _ := event["type"].(string)
		if eventType == "typing_indicator" {
			// Broadcast typing indicator to chat participants
			chatID, _ := event["chat_id"].(string)
			userID, _ := event["user_id"].(string)
			action, _ := event["action"].(string)
			
			// Get chat participants from Redis or service
			// For now, broadcast to all connected clients (simplified)
			outgoing := OutgoingMessage{
				Type:      "typing",
				Timestamp: time.Now(),
				Payload: map[string]string{
					"chat_id": chatID,
					"user_id": userID,
					"action":  action,
				},
			}
			outgoingJSON, _ := json.Marshal(outgoing)
			
			s.hub.mu.RLock()
			for uid, client := range s.hub.clients {
				if uid != userID { // Don't send to sender
					select {
					case client.send <- outgoingJSON:
					default:
					}
				}
			}
			s.hub.mu.RUnlock()
		}
	}
}

// Consume notification events from Kafka
func (s *Server) consumeNotificationEvents() {
	partitionConsumer, err := s.kafka.ConsumePartition("notification-events", 0, sarama.OffsetNewest)
	if err != nil {
		log.Printf("Failed to consume notification events: %v", err)
		return
	}
	defer partitionConsumer.Close()
	
	for msg := range partitionConsumer.Messages() {
		var event map[string]interface{}
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			continue
		}
		
		// Check if recipients are online
		recipients, _ := event["recipients"].([]interface{})
		for _, recipient := range recipients {
			recipientID, _ := recipient.(string)
			
			s.hub.mu.RLock()
			_, online := s.hub.clients[recipientID]
			s.hub.mu.RUnlock()
			
			if !online {
				// User is offline, trigger push notification
				s.triggerPushNotification(recipientID, event)
			}
		}
	}
}

// Trigger push notification for offline user
func (s *Server) triggerPushNotification(userID string, event map[string]interface{}) {
	// Publish to push notification topic
	pushEvent := map[string]interface{}{
		"type":     "push_notification",
		"user_id":  userID,
		"event":    event,
		"priority": "high",
	}
	pushJSON, _ := json.Marshal(pushEvent)
	
	kafkaMsg := &sarama.ProducerMessage{
		Topic: "push-notifications",
		Value: sarama.ByteEncoder(pushJSON),
	}
	s.kafkaProd.SendMessage(kafkaMsg)
}

// Get user presence
func (s *Server) getUserPresence(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	
	ctx := r.Context()
	presenceData, err := s.redis.Get(ctx, fmt.Sprintf("presence:%s", userID)).Result()
	if err != nil {
		// Return offline status
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"user_id":    userID,
			"status":     "offline",
			"last_seen":  nil,
		})
		return
	}
	
	var presence map[string]interface{}
	json.Unmarshal([]byte(presenceData), &presence)
	presence["user_id"] = userID
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(presence)
}

// Update presence
func (s *Server) updatePresence(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	userID := claims["user_id"].(string)
	
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	
	ctx := r.Context()
	presenceData := map[string]interface{}{
		"status":      req.Status,
		"last_seen":   time.Now().Unix(),
		"device_type": "api",
	}
	presenceJSON, _ := json.Marshal(presenceData)
	
	expiration := 5 * time.Minute
	if req.Status == "offline" {
		expiration = 24 * time.Hour
	}
	
	s.redis.Set(ctx, fmt.Sprintf("presence:%s", userID), presenceJSON, expiration)
	s.publishPresenceEvent(userID, req.Status)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
