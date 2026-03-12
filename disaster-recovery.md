# Disaster Recovery Plan

## Overview

This document outlines the disaster recovery procedures for the real-time messaging platform, ensuring business continuity and minimal data loss.

## Recovery Objectives

| Service | RPO (Recovery Point Objective) | RTO (Recovery Time Objective) |
|---------|-------------------------------|-------------------------------|
| User Authentication | 0 minutes | 5 minutes |
| Messaging | 1 minute | 10 minutes |
| User Profiles | 5 minutes | 15 minutes |
| Media Storage | 1 hour | 30 minutes |
| Search Index | 24 hours | 2 hours |

## Architecture Redundancy

### Multi-Region Deployment

```
┌────────────────────────────────────────────────────────────────┐
│                         GLOBAL TRAFFIC                          │
│                      MANAGER (Cloudflare/AWS)                   │
│                     Geo-Routing + Health Checks                 │
└────────────────────────────────┬───────────────────────────────┘
                                 │
        ┌────────────────────────┼────────────────────────┐
        │                        │                        │
   ┌────▼────┐             ┌────▼────┐             ┌────▼────┐
   │Primary  │◄───────────►│Secondary│◄───────────►│  DR     │
   │Region   │   Sync      │ Region  │   Async     │ Region  │
   │(Active) │             │(Active) │             │(Standby)│
   └─────────┘             └─────────┘             └─────────┘
   us-east-1               eu-west-1               ap-south-1
```

### Component Redundancy

#### WebSocket Gateways
- Minimum 3 pods per region
- Distributed across availability zones
- Session affinity for connection stability

#### Databases
- PostgreSQL: 3-node HA cluster per region
- Cassandra: 3+ nodes per region, RF=3
- Redis: 6-node cluster (3 masters + 3 replicas)

#### Message Queue
- Kafka: 3+ brokers per region
- Replication factor: 3
- Min ISR: 2

## Backup Procedures

### PostgreSQL Backups

```bash
# Continuous archiving (WAL)
archive_mode = on
archive_command = 'aws s3 cp %p s3://messaging-backups/wal/%f'
archive_timeout = 60

# Daily base backup
0 2 * * * pg_basebackup -D /backups/$(date +%Y%m%d) -Ft -z -P

# Weekly full backup to S3
0 3 * * 0 aws s3 sync /backups/ s3://messaging-backups/postgres/
```

### Cassandra Backups

```bash
# Snapshot backup daily
nodetool snapshot messaging
aws s3 sync /var/lib/cassandra/data/messaging/*/snapshots/ s3://messaging-backups/cassandra/

# Incremental backups
nodetool enableincrementalbackup
```

### Redis Backups

```bash
# RDB snapshots every 15 minutes
save 900 1
save 300 10
save 60 10000

# AOF for durability
appendonly yes
appendfsync everysec

# Backup to S3
aws s3 cp /data/dump.rdb s3://messaging-backups/redis/
```

### Media Storage

- Cross-region replication enabled
- S3 versioning for accidental deletion
- Lifecycle policies for cost optimization

## Failover Procedures

### Database Failover

#### PostgreSQL Failover (Patroni)

```bash
# Automatic failover
# Patroni handles leader election automatically

# Manual failover (maintenance)
patronictl failover messaging-cluster

# Check cluster status
patronictl list messaging-cluster
```

#### Cassandra Failover

```bash
# Cassandra is self-healing
# Just ensure sufficient nodes are available

# Check node status
nodetool status

# Repair after node recovery
nodetool repair -full
```

### Service Failover

#### Kubernetes Failover

```bash
# Check pod status
kubectl get pods -n messaging-platform

# Force rollout restart
kubectl rollout restart deployment/user-service -n messaging-platform

# Scale up during issues
kubectl scale deployment/user-service --replicas=10 -n messaging-platform
```

#### Regional Failover

```bash
# Update DNS to point to secondary region
aws route53 change-resource-record-sets \
  --hosted-zone-id Z123456789 \
  --change-batch file://failover-to-secondary.json

# Or update load balancer weights
aws elbv2 modify-target-group-attributes \
  --target-group-arn arn:aws:elasticloadbalancing:... \
  --attributes Key=deregistration_delay.timeout_seconds,Value=30
```

## Disaster Scenarios

### Scenario 1: Single Pod Failure

**Impact:** Minimal
**Detection:** Health check failure
**Response:** Automatic

```bash
# Kubernetes automatically restarts failed pods
# Check events
kubectl get events -n messaging-platform --field-selector reason=Failed

# View pod logs
kubectl logs deployment/user-service -n messaging-platform --previous
```

### Scenario 2: Node Failure

**Impact:** Moderate
**Detection:** Node NotReady
**Response:** Automatic pod rescheduling

```bash
# Check node status
kubectl get nodes

# Cordon failed node
kubectl cordon <node-name>

# Drain node (if recoverable)
kubectl drain <node-name> --ignore-daemonsets --delete-emptydir-data

# Pods automatically rescheduled to healthy nodes
```

### Scenario 3: Availability Zone Failure

**Impact:** High
**Detection:** Multiple service failures in AZ
**Response:** Automatic failover to other AZs

```bash
# Verify pod distribution
kubectl get pods -n messaging-platform -o wide

# Check anti-affinity rules are working
kubectl describe pod <pod-name> | grep -A 10 Affinity

# Scale up in healthy AZs if needed
kubectl scale deployment/websocket-gateway --replicas=20 -n messaging-platform
```

### Scenario 4: Database Primary Failure

**Impact:** High
**Detection:** Connection failures, replication lag
**Response:** Automatic failover

```bash
# PostgreSQL (Patroni)
# Automatic failover in < 30 seconds

# Verify new leader
patronictl list

# Update application connections (if needed)
kubectl rollout restart deployment/user-service -n messaging-platform
```

### Scenario 5: Complete Region Failure

**Impact:** Critical
**Detection:** Health check failures from all services
**Response:** DNS failover to secondary region

```bash
# 1. Verify secondary region is healthy
kubectl --context=secondary get pods -n messaging-platform

# 2. Update DNS records (manual or automated)
# Using Route53 failover routing

# 3. Promote secondary database (if needed)
# PostgreSQL: Patroni switchover
patronictl switchover messaging-cluster --master secondary-region-leader

# 4. Verify application functionality
curl https://api.messaging.app/health

# 5. Monitor error rates and latency
```

### Scenario 6: Data Corruption

**Impact:** Critical
**Detection:** Data validation failures, user reports
**Response:** Point-in-time recovery

```bash
# PostgreSQL PITR
# 1. Stop application writes
kubectl scale deployment/messaging-service --replicas=0 -n messaging-platform

# 2. Restore from backup
pg_restore --host=new-instance --dbname=messaging backup.dump

# 3. Apply WAL logs to specific point
pg_waldump --timeline=1 --start=0/12345678

# 4. Verify data integrity
psql -c "SELECT COUNT(*) FROM users;"

# 5. Resume services
kubectl scale deployment/messaging-service --replicas=10 -n messaging-platform
```

## Recovery Playbooks

### Playbook 1: Service Degradation

1. **Identify affected services**
   ```bash
   kubectl get pods -n messaging-platform | grep -v Running
   ```

2. **Check resource utilization**
   ```bash
   kubectl top pods -n messaging-platform
   kubectl top nodes
   ```

3. **Scale up if resource constrained**
   ```bash
   kubectl scale deployment/<service> --replicas=<higher> -n messaging-platform
   ```

4. **Check logs for errors**
   ```bash
   kubectl logs deployment/<service> -n messaging-platform --tail=100
   ```

5. **Restart if needed**
   ```bash
   kubectl rollout restart deployment/<service> -n messaging-platform
   ```

### Playbook 2: Database Recovery

1. **Assess damage**
   ```bash
   # Check replication lag
   psql -c "SELECT * FROM pg_stat_replication;"
   
   # Check for data corruption
   psql -c "SELECT pg_database.datname, pg_database_size(pg_database.datname) FROM pg_database;"
   ```

2. **Choose recovery method**
   - Minor corruption: Restore from replica
   - Major corruption: PITR from backup
   - Complete loss: Full restore + WAL replay

3. **Execute recovery**
   ```bash
   # Example: Restore from backup
   aws s3 cp s3://messaging-backups/postgres/latest.dump - | pg_restore --clean --if-exists --dbname=messaging
   ```

4. **Verify recovery**
   ```bash
   # Run data validation scripts
   ./scripts/validate-data.sh
   ```

5. **Resume services**
   ```bash
   kubectl rollout restart deployment/messaging-service -n messaging-platform
   ```

## Testing Procedures

### Chaos Engineering

```bash
# Install Chaos Mesh
helm repo add chaos-mesh https://charts.chaos-mesh.org
helm install chaos-mesh chaos-mesh/chaos-mesh -n chaos-testing --create-namespace

# Pod failure experiment
kubectl apply -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: PodChaos
metadata:
  name: user-service-failure
  namespace: chaos-testing
spec:
  action: pod-failure
  mode: one
  duration: 5m
  selector:
    namespaces:
      - messaging-platform
    labelSelectors:
      app: user-service
EOF

# Network partition experiment
kubectl apply -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: network-partition
  namespace: chaos-testing
spec:
  action: partition
  mode: all
  selector:
    namespaces:
      - messaging-platform
  duration: 10m
  direction: both
  target:
    selector:
      namespaces:
        - messaging-platform
      labelSelectors:
        app: postgres
    mode: one
EOF
```

### Disaster Recovery Drills

**Monthly:**
- Single pod failure recovery
- Node failure recovery

**Quarterly:**
- AZ failure simulation
- Database failover

**Annually:**
- Full region failover
- Complete data restore

## Communication Plan

### Incident Response Team

| Role | Responsibility | Contact |
|------|---------------|---------|
| Incident Commander | Overall coordination | On-call manager |
| Technical Lead | Technical decisions | Senior engineer |
| Communications | External communication | PR team |
| Customer Support | User communication | Support lead |

### Notification Channels

1. **Internal:**
   - PagerDuty for critical alerts
   - Slack for team coordination
   - Video bridge for major incidents

2. **External:**
   - Status page updates
   - Twitter for major outages
   - In-app notifications for affected users

## Post-Incident Review

### Template

1. **Summary**
   - What happened?
   - When did it start?
   - When was it resolved?

2. **Impact**
   - Users affected
   - Services impacted
   - Data loss (if any)

3. **Timeline**
   - Detection time
   - Response time
   - Resolution time

4. **Root Cause**
   - Technical analysis
   - Contributing factors

5. **Lessons Learned**
   - What went well?
   - What could be improved?

6. **Action Items**
   - Preventive measures
   - Detection improvements
   - Response improvements

## Documentation Maintenance

This document should be reviewed and updated:
- After every significant infrastructure change
- After every disaster recovery drill
- Quarterly scheduled review

Last updated: 2024
Next review: 2024-Q2
