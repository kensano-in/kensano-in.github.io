# Real-Time Messaging Platform - Architecture Specification

## Executive Summary

This document provides a comprehensive specification for a production-grade, large-scale real-time messaging platform designed to support tens of millions of users with features comparable to Telegram or Signal.

### Key Capabilities
- **Real-time messaging** with sub-second delivery
- **End-to-end encryption** for privacy
- **Horizontal scalability** to handle 50M+ DAU
- **Multi-region deployment** for global availability
- **99.99% uptime** with comprehensive disaster recovery

---

## Table of Contents

1. [System Overview](#system-overview)
2. [Architecture Layers](#architecture-layers)
3. [Core Microservices](#core-microservices)
4. [Data Storage](#data-storage)
5. [Message Routing](#message-routing)
6. [Security Architecture](#security-architecture)
7. [Scaling Strategy](#scaling-strategy)
8. [Deployment & Operations](#deployment--operations)
9. [Monitoring & Observability](#monitoring--observability)

---

## System Overview

### High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              CLIENT LAYER                                    │
│   ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐                        │
│   │ Web App │  │ Android │  │   iOS   │  │ Desktop │                        │
│   └────┬────┘  └────┬────┘  └────┬────┘  └────┬────┘                        │
└────────┼────────────┼────────────┼────────────┼──────────────────────────────┘
         │            │            │            │
         └────────────┴────────────┴────────────┘
                          │
┌─────────────────────────▼──────────────────────────────────────────────────┐
│                         EDGE NETWORK LAYER                                  │
│   ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐       │
│   │  CDN Nodes  │  │ WebSocket   │  │ Regional LB │  │ Anycast DNS │       │
│   │             │  │   Edge      │  │             │  │             │       │
│   └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘       │
└─────────────────────────┬──────────────────────────────────────────────────┘
                          │
┌─────────────────────────▼──────────────────────────────────────────────────┐
│                         API GATEWAY LAYER                                   │
│   ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐       │
│   │ Kong/Envoy  │  │ Rate Limiter│  │ Auth Filter │  │   Router    │       │
│   └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘       │
└─────────────────────────┬──────────────────────────────────────────────────┘
                          │
┌─────────────────────────▼──────────────────────────────────────────────────┐
│                      CORE MICROSERVICES LAYER                               │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐          │
│  │  User    │ │   Auth   │ │ Messaging│ │  Group   │ │ Channel  │          │
│  │ Service  │ │ Service  │ │ Service  │ │ Service  │ │ Service  │          │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘ └──────────┘          │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐          │
│  │  Media   │ │Notification│ │ Search   │ │   AI     │ │ Presence │          │
│  │ Service  │ │ Service  │ │ Service  │ │ Service  │ │ Service  │          │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘ └──────────┘          │
└─────────────────────────┬──────────────────────────────────────────────────┘
                          │
┌─────────────────────────▼──────────────────────────────────────────────────┐
│                   MESSAGING INFRASTRUCTURE LAYER                            │
│   ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐       │
│   │ Apache Kafka│  │  RabbitMQ   │  │ Redis Pub/Sub│  │Msg Router   │       │
│   └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘       │
└─────────────────────────┬──────────────────────────────────────────────────┘
                          │
┌─────────────────────────▼──────────────────────────────────────────────────┐
│                        DATA STORAGE LAYER                                   │
│   ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐       │
│   │ PostgreSQL  │  │  Cassandra  │  │ Redis Cache │  │Elasticsearch│       │
│   │  (Users)    │  │ (Messages)  │  │  (Sessions) │  │  (Search)   │       │
│   └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘       │
└────────────────────────────────────────────────────────────────────────────┘
```

### Technology Stack

| Layer | Technology | Purpose |
|-------|------------|---------|
| API Gateway | Kong / Envoy | Routing, Auth, Rate Limiting |
| Services | Go (Golang) | High-performance microservices |
| Messaging | Apache Kafka | Event streaming |
| Cache | Redis | Sessions, Presence, Rate limiting |
| User DB | PostgreSQL | Relational user data |
| Message DB | Cassandra | Time-series message storage |
| Search | Elasticsearch | Full-text search |
| Media | S3 / GCS | Object storage |
| Container | Kubernetes | Orchestration |
| Monitoring | Prometheus + Grafana | Metrics |
| Logging | ELK Stack | Centralized logging |

---

## Architecture Layers

### 1. Client Layer

**Supported Platforms:**
- Web Application (React/Vue.js)
- Android (Kotlin)
- iOS (Swift)
- Desktop (Electron/React Native)

**Client Capabilities:**
- WebSocket connection management
- End-to-end encryption (Signal Protocol)
- Offline message queue
- Media upload/download
- Push notification handling

### 2. Edge Network Layer

**Components:**
- **CDN Nodes**: CloudFlare/AWS CloudFront for static assets
- **WebSocket Edge**: Regional connection termination
- **Regional Load Balancers**: Distribute traffic
- **Anycast DNS**: Route to nearest region

**Purpose:**
- Reduce latency (< 50ms to edge)
- DDoS protection
- TLS termination
- Geographic routing

### 3. API Gateway Layer

**Responsibilities:**
- Authentication verification (JWT)
- Rate limiting (100 req/min anonymous, 1000 req/min authenticated)
- Request routing
- API versioning
- Request/response logging
- SSL/TLS handling

**Tools:**
- Kong Gateway
- Envoy Proxy
- NGINX (fallback)

### 4. Core Microservices Layer

#### User Service
- User profile management
- Contact management
- Privacy settings
- User search

#### Authentication Service
- Login/logout
- Session management
- 2FA (TOTP)
- JWT token issuance

#### Messaging Service
- Message sending/receiving
- Message history
- Delivery status tracking
- Message editing/deletion

#### Group Service
- Group creation/management
- Member management
- Permissions
- Invite links

#### Channel Service
- Broadcast channels
- Subscriber management
- Channel discovery

#### Media Service
- File upload handling
- Image/video compression
- Thumbnail generation
- CDN integration

#### Notification Service
- Push notification delivery (FCM/APNS)
- In-app notifications
- Notification preferences

#### Search Service
- User search
- Message search
- Full-text indexing

#### AI Service (Optional)
- Spam detection
- Smart replies
- Content moderation

#### Presence Service
- Online status tracking
- Last seen
- Typing indicators

### 5. Messaging Infrastructure Layer

**Apache Kafka:**
- Topics: `message-events`, `notification-events`, `presence-events`
- Partitions: 100+ per topic
- Replication factor: 3
- Retention: 7 days

**Redis Pub/Sub:**
- Real-time presence updates
- Typing indicators
- Session management

### 6. Data Storage Layer

**PostgreSQL (User Data):**
- User profiles
- Authentication data
- Contacts
- Privacy settings
- 3-node HA cluster with read replicas

**Cassandra (Messages):**
- Message history
- Chat metadata
- Message status
- Time-series optimized
- 12+ node cluster, RF=3

**Redis (Cache):**
- Session tokens
- Presence states
- Rate limiting counters
- 6-node cluster

**Elasticsearch (Search):**
- User search index
- Message search index
- Media metadata
- 3-node cluster

---

## Core Microservices

### User Service

**API Endpoints:**
```
GET    /api/v1/users/me              # Get current user
PUT    /api/v1/users/me              # Update profile
GET    /api/v1/users/{id}            # Get user by ID
GET    /api/v1/users/username/{name} # Get user by username
POST   /api/v1/users/search          # Search users
GET    /api/v1/contacts              # List contacts
POST   /api/v1/contacts              # Add contact
DELETE /api/v1/contacts/{id}         # Remove contact
GET    /api/v1/privacy               # Get privacy settings
PUT    /api/v1/privacy               # Update privacy
```

**Database Schema:**
- `users` - User profiles
- `contacts` - Contact relationships
- `privacy_settings` - User privacy configuration

### Auth Service

**API Endpoints:**
```
POST   /api/v1/auth/register         # User registration
POST   /api/v1/auth/login            # User login
POST   /api/v1/auth/refresh          # Refresh token
POST   /api/v1/auth/logout           # Logout
POST   /api/v1/auth/2fa/setup        # Setup 2FA
POST   /api/v1/auth/2fa/verify       # Verify 2FA
GET    /api/v1/auth/sessions         # List sessions
DELETE /api/v1/auth/sessions/{id}    # Revoke session
```

**Security Features:**
- bcrypt password hashing
- JWT access tokens (7-day expiry)
- TOTP-based 2FA
- Rate limiting on login attempts
- Session management

### Messaging Service

**API Endpoints:**
```
POST   /api/v1/chats                 # Create chat
GET    /api/v1/chats                 # List chats
GET    /api/v1/chats/{id}            # Get chat details
POST   /api/v1/chats/{id}/messages   # Send message
GET    /api/v1/chats/{id}/messages   # Get messages
PUT    /api/v1/chats/{id}/messages/{msgId}  # Edit message
DELETE /api/v1/chats/{id}/messages/{msgId}  # Delete message
POST   /api/v1/chats/{id}/messages/{msgId}/read  # Mark read
```

**Message Flow:**
1. Client sends message via WebSocket
2. Gateway authenticates and forwards
3. Messaging Service validates and stores
4. Kafka event published
5. Delivery service routes to recipients
6. Push notification for offline users

### WebSocket Gateway

**Features:**
- Persistent connections (10,000+ per pod)
- Heartbeat/ping-pong
- Automatic reconnection
- Message acknowledgment
- Presence tracking

**Connection URL:**
```
wss://ws.messaging.app/ws?token={jwt_token}&device_id={device_id}
```

**Message Types:**
```json
{"type": "message", "payload": {"chat_id": "...", "content": "..."}}
{"type": "typing", "payload": {"chat_id": "...", "action": "typing"}}
{"type": "presence", "payload": {"status": "online"}}
{"type": "read_receipt", "payload": {"chat_id": "...", "message_id": "..."}}
```

---

## Data Storage

### PostgreSQL Schema

**users table:**
```sql
CREATE TABLE users (
    user_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(32) UNIQUE NOT NULL,
    email VARCHAR(255) UNIQUE,
    phone VARCHAR(20) UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    display_name VARCHAR(100),
    avatar_url TEXT,
    status VARCHAR(20) DEFAULT 'active',
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    last_seen TIMESTAMP,
    is_verified BOOLEAN DEFAULT FALSE
);
```

### Cassandra Schema

**messages_by_chat:**
```sql
CREATE TABLE messages_by_chat (
    chat_id UUID,
    message_id TIMEUUID,
    sender_id UUID,
    message_type VARCHAR(20),
    content TEXT,
    encrypted_payload BLOB,
    created_at TIMESTAMP,
    edited_at TIMESTAMP,
    is_deleted BOOLEAN,
    reply_to TIMEUUID,
    PRIMARY KEY (chat_id, message_id)
) WITH CLUSTERING ORDER BY (message_id DESC);
```

### Redis Data Structures

**Session Cache:**
```
Key: session:{token}
Type: Hash
Fields: user_id, device_id, expires_at
TTL: 7 days
```

**Presence:**
```
Key: presence:{user_id}
Type: Hash
Fields: status, last_seen, device_type
TTL: 5 minutes
```

---

## Message Routing

### Message Flow Architecture

```
┌─────────┐    ┌─────────────┐    ┌──────────────┐    ┌─────────────┐
│ Client  │───►│   Edge      │───►│ API Gateway  │───►│  WebSocket  │
│ Sender  │    │   Gateway   │    │  (Kong)      │    │   Gateway   │
└─────────┘    └─────────────┘    └──────────────┘    └──────┬──────┘
                                                               │
┌─────────┐    ┌─────────────┐    ┌──────────────┐           │
│ Client  │◄───│   Kafka     │◄───│  Messaging   │◄──────────┘
│Recipient│    │   Consumer  │    │   Service    │
└─────────┘    └─────────────┘    └──────────────┘
```

### Delivery Guarantees

**At-Least-Once Delivery:**
- Messages persisted before acknowledgment
- Kafka consumer groups for reliability
- Retry logic for failed deliveries

**Ordering Guarantees:**
- Messages ordered by chat (Cassandra clustering key)
- Kafka partition per chat for ordered processing

---

## Security Architecture

### Transport Security
- TLS 1.3 for all connections
- Certificate pinning in mobile apps
- HSTS headers

### Application Security
- JWT tokens with short expiry
- Refresh token rotation
- Rate limiting per user/IP
- Input validation and sanitization

### End-to-End Encryption
- Signal Protocol implementation
- Client-side encryption
- Server stores only encrypted payloads
- Key exchange via X3DH

### Abuse Prevention
- Spam detection (ML-based)
- Rate limiting
- CAPTCHA for suspicious activity
- Content moderation

---

## Scaling Strategy

See [scaling-strategy.md](scaling-strategy.md) for detailed information.

### Key Metrics

| Metric | Target |
|--------|--------|
| Concurrent Connections | 10M+ |
| Messages/Second | 500K peak |
| API Response Time | < 100ms p99 |
| Message Delivery | < 500ms |
| Availability | 99.99% |

### Auto-Scaling Configuration

**WebSocket Gateway:**
```yaml
minReplicas: 50
maxReplicas: 500
metrics:
  - type: Pods
    pods:
      metricName: websocket_connections
      targetAverageValue: 8000
```

---

## Deployment & Operations

### CI/CD Pipeline

```
Code Commit → Lint/Test → Security Scan → Build Image → Push to Registry
                                                    ↓
Deploy Staging → Integration Tests → Deploy Production (Canary)
```

### Kubernetes Deployment

```bash
# Apply all manifests
kubectl apply -f infrastructure/kubernetes/

# Check deployment status
kubectl get pods -n messaging-platform

# View logs
kubectl logs -f deployment/user-service -n messaging-platform
```

### Environment Configuration

| Environment | Purpose | URL |
|-------------|---------|-----|
| Development | Local testing | localhost |
| Staging | Pre-production | api-staging.messaging.app |
| Production | Live traffic | api.messaging.app |

---

## Monitoring & Observability

### Metrics (Prometheus)

**Infrastructure:**
- CPU/Memory/Disk usage
- Network I/O
- Pod restart count

**Application:**
- Request latency (p50, p95, p99)
- Error rates
- Message throughput
- Active connections

**Business:**
- DAU/MAU
- Messages per user
- Session duration

### Logging (ELK Stack)

**Log Levels:**
- ERROR: Service errors, exceptions
- WARN: Degraded performance, retries
- INFO: Significant events
- DEBUG: Detailed debugging (dev only)

### Alerting

**Critical Alerts:**
- Service down
- Database failure
- High error rate (> 5%)
- Latency spike (> 1s p99)

**Warning Alerts:**
- High CPU (> 80%)
- High memory (> 85%)
- Kafka lag (> 10000)

---

## Disaster Recovery

See [disaster-recovery.md](disaster-recovery.md) for detailed procedures.

### RPO/RTO

| Component | RPO | RTO |
|-----------|-----|-----|
| User Data | 0 min | 5 min |
| Messages | 1 min | 10 min |
| Sessions | 0 min | 15 min |

### Backup Strategy
- PostgreSQL: Continuous WAL archiving + daily snapshots
- Cassandra: Daily snapshots
- Redis: RDB snapshots every 15 minutes
- Media: Cross-region S3 replication

---

## Conclusion

This architecture provides a robust, scalable foundation for a real-time messaging platform capable of serving tens of millions of users. The microservices approach enables independent scaling and deployment, while the multi-layered security ensures user privacy and data protection.

### Next Steps

1. Implement core services (User, Auth, Messaging)
2. Set up Kubernetes infrastructure
3. Deploy monitoring stack
4. Conduct load testing
5. Implement disaster recovery procedures

---

## References

- [Scaling Strategy](scaling-strategy.md)
- [Disaster Recovery](disaster-recovery.md)
- [API Documentation](api-reference.md)
- [Deployment Guide](deployment-guide.md)
