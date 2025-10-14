package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	connStr      = "postgres://replicator:123456a@@localhost:5432/mydb?replication=database"
	slotName     = "mydb_slot"
	outputPlugin = "pgoutput"
	publication  = "mydb_pub"
)

var event = struct {
	relation string
	columns  []string
}{}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	conn, err := pgconn.Connect(ctx, connStr)
	if err != nil {
		panic(err)
	}
	defer conn.Close(ctx)

	// Optional: Ensure test table exists (run once)
	_, err = conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS test_entities (id SERIAL PRIMARY KEY, name TEXT, neighbors TEXT[]);").ReadAll()
	if err != nil {
		panic(err)
	}

	// Create slot if needed (skip if already done)
	_, err = pglogrepl.CreateReplicationSlot(ctx, conn, slotName, outputPlugin, pglogrepl.CreateReplicationSlotOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		panic(err)
	}

	// Start replication
	var lsn pglogrepl.LSN
	pluginArgs := []string{"proto_version '1'", fmt.Sprintf("publication_names '%s'", publication)}
	err = pglogrepl.StartReplication(ctx, conn, slotName, lsn, pglogrepl.StartReplicationOptions{PluginArgs: pluginArgs})
	if err != nil {
		panic(err)
	}

	pingTime := time.Now()
	for ctx.Err() != context.Canceled {
		if time.Since(pingTime) > 10*time.Second {
			err = pglogrepl.SendStandbyStatusUpdate(ctx, conn, pglogrepl.StandbyStatusUpdate{
				WALWritePosition: lsn,
			})
			if err != nil {
				panic(err)
			}
			pingTime = time.Now()
		}

		ctxWithTimeout, cancelTimeout := context.WithTimeout(ctx, 10*time.Second)
		msg, err := conn.ReceiveMessage(ctxWithTimeout)
		cancelTimeout()
		if pgconn.Timeout(err) {
			continue
		}
		if err != nil {
			panic(err)
		}

		switch msg := msg.(type) {
		case *pgproto3.CopyData:
			switch msg.Data[0] {
			case pglogrepl.PrimaryKeepaliveMessageByteID:
				// Ignore
			case pglogrepl.XLogDataByteID:
				xlog, err := pglogrepl.ParseXLogData(msg.Data[1:])
				if err != nil {
					panic(err)
				}

				// Parse the WAL data into a logical replication message
				logicalMsg, err := pglogrepl.Parse(xlog.WALData)
				if err != nil {
					panic(err)
				}

				switch m := logicalMsg.(type) {
				case *pglogrepl.RelationMessage:
					event.columns = make([]string, len(m.Columns))
					for i, col := range m.Columns {
						event.columns[i] = col.Name
					}
					event.relation = m.Namespace + "." + m.RelationName

				case *pglogrepl.InsertMessage:
					printChange("INSERT", m.Tuple)

				case *pglogrepl.UpdateMessage:
					printChange("UPDATE", m.NewTuple)

				case *pglogrepl.DeleteMessage:
					printChange("DELETE", m.OldTuple)

				case *pglogrepl.TruncateMessage:
					fmt.Printf("TRUNCATE: %v\n", m.RelationIDs)
				}

				// WALSize was removed → use len(WALData)
				lsn = xlog.WALStart + pglogrepl.LSN(len(xlog.WALData))
			}
		}
	}
}

func printChange(op string, tuple *pglogrepl.TupleData) {
	if tuple == nil {
		fmt.Printf("%s %s (nil tuple)\n", op, event.relation)
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s %s (", op, event.relation))
	for i, col := range tuple.Columns {
		if col.Data != nil {
			sb.WriteString(fmt.Sprintf("%s: %s ", event.columns[i], string(col.Data)))
		}
	}
	sb.WriteString(")")
	fmt.Println(sb.String())
}
