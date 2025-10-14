# prepare for read log from WAL
# 1. Edit postgresql.conf (Core WAL settings)
- sudo gedit /etc/postresql/12/main/postgresql.conf
- ADD below text to .conf
`````````````````````````BEGIN`````````````````` 
# Enable logical decoding (required for WAL-based CDC)
wal_level = logical  # Default: replica; Options: minimal, replica, logical

# Allow replication connections
max_wal_senders = 10  # Number of WAL sender processes; Set to 2x your expected slots

# Replication slots management
max_replication_slots = 5  # Max logical slots; Start with 1-5 for testing

# Optional: Tune WAL retention for safety (prevents slot from advancing too fast)
wal_sender_timeout = 60s  # Timeout for idle senders
hot_standby_feedback = on  # If using hot standby replicas (optional)

# Optional: For high-throughput (adjust based on your workload)
max_logical_replication_workers = 10  # Max workers for logical replication
``````````````````````````END`````````````````````````
# 2. Edit pg_hba.conf (Access Control for Replication)
- sudo gedit /etx/postgresql/12/main/pg_hba.conf
- Add a line to allow the replication user

# TYPE  DATABASE        USER            ADDRESS                 METHOD
local   replication     replicator                              peer  # Local connections
host    replication     replicator      127.0.0.1/32            md5   # IPv4 local
host    replication     replicator      ::1/128                 md5   # IPv6 local

# 3. restart Postsql
- sudo systemctl restart postgresql

# 4. create Replication User and Permissions
- 1. sudo -u postgres psql
- 2. in postsql bash run:

## -- Create user with replication privileges
CREATE USER replicator WITH REPLICATION PASSWORD 'your_secure_password';

## -- Grant usage on schema (required for pgoutput in v15+)
GRANT USAGE ON SCHEMA public TO replicator;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO replicator;  -- If publishing specific tables

## -- Optional: For all schemas/tables if multi-schema
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO replicator;

# 5. create a publication 
# 1. still in psql
## -- Drop if exists (for clean start)
DROP PUBLICATION IF EXISTS mypub;

## -- Create publication for all tables (or specify: FOR TABLE entities, relationships)
CREATE PUBLICATION mypub FOR ALL TABLES;

## -- Verify
\dRp mypub

# 6. create Replication Slot
# still in psql bash:
## -- Create slot with pgoutput plugin (built-in logical decoder)
SELECT pg_create_logical_replication_slot('my_slot', 'pgoutput');

## -- Verify slot exists and is active
SELECT * FROM pg_replication_slots WHERE slot_name = 'my_slot';
# slot_name |  plugin  | slot_type | datoid | database | temporary | active | active_pid | xmin | catalog_xmin | restart_lsn | confirmed_flush_lsn 
# -----------+----------+-----------+--------+----------+-----------+--------+------------+------+--------------+-------------+---------------------
# my_slot   | pgoutput | logical   |  13463 | postgres | f         | t      |     156817 |      |          657 | 0/1899B08   | 0/1899D22
# (1 row)


`````````````````````````````AFTER ALL ````````````````````````````````
## after all before using all opon command, now can creat stream change WAL to other db like:
# 1. connect to mydb
sudo -i -u postgres
psql -d mydb
# 2. Then create a replication slot for mydb:
SELECT pg_create_logical_replication_slot('mydb_slot', 'pgoutput');
# 3. Verify:
SELECT slot_name, database, plugin, active
FROM pg_replication_slots;

# slot_name | database | plugin   | active
# -----------+----------+----------+--------
# mydb_slot  | mydb     | pgoutput | f

# 4. Also ensure you have a publication for mydb
CREATE PUBLICATION mydb_pub FOR ALL TABLES;

## 5. Config in go client to read log:
<!-- const (
	connStr      = "postgres://replicator:123456a@@localhost:5432/mydb?replication=database"
	slotName     = "mydb_slot"
	outputPlugin = "pgoutput"
	publication  = "mydb_pub"
) -->