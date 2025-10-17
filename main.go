package main

import (
	"context"
	"fmt"
	"log"
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
	slotName     = "mydb_slot2"
	outputPlugin = "pgoutput"
	publication  = "mydb_pub"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	conn, err := pgconn.Connect(ctx, connStr)
	if err != nil {
		panic(err)
	}
	defer conn.Close(ctx)

	// Create slot if needed (skip if already done)
	_, err = pglogrepl.CreateReplicationSlot(ctx, conn, slotName, outputPlugin, pglogrepl.CreateReplicationSlotOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		panic(err)
	}

	// Start replication
	var lsn pglogrepl.LSN
	relationStore := make(map[uint32]*pglogrepl.RelationMessage)

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

		if err != nil {
			if pgconn.Timeout(err) {
				continue
			}
			log.Fatalf("receive error: %v", err)
		}

		switch msg := msg.(type) {
		case *pgproto3.CopyData:
			switch msg.Data[0] {

			case pglogrepl.PrimaryKeepaliveMessageByteID:
				// Ignore keepalive messages for now
				continue

			case pglogrepl.XLogDataByteID:
				xlog, err := pglogrepl.ParseXLogData(msg.Data[1:])
				if err != nil {
					log.Printf("parse xlog error: %v", err)
					continue
				}

				logicalMsg, err := pglogrepl.Parse(xlog.WALData)
				if err != nil {
					log.Printf("parse logical msg error: %v", err)
					continue
				}

				switch m := logicalMsg.(type) {

				case *pglogrepl.RelationMessage:
					relationStore[m.RelationID] = m
					fmt.Printf("🧩 Relation cached: %s.%s (cols: %d)\n",
						m.Namespace, m.RelationName, len(m.Columns))

				case *pglogrepl.InsertMessage:
					if rel, ok := relationStore[m.RelationID]; ok {
						printChange("INSERT", rel, m.Tuple)
					} else {
						log.Printf("unknown relation ID: %d", m.RelationID)
					}

				case *pglogrepl.UpdateMessage:
					if rel, ok := relationStore[m.RelationID]; ok {
						printChange("UPDATE", rel, m.NewTuple)
					}

				case *pglogrepl.DeleteMessage:
					if rel, ok := relationStore[m.RelationID]; ok {
						printChange("DELETE", rel, m.OldTuple)
					}

				case *pglogrepl.TruncateMessage:
					fmt.Printf("TRUNCATE: %v\n", m.RelationIDs)

				default:
					fmt.Printf("Unhandled logical message: %T\n", m)
				}

				// Update last seen LSN
				lsn = xlog.WALStart + pglogrepl.LSN(len(xlog.WALData))
			}
		}
	}
}

type DBEvent struct {
	DBName string      `json:"db_name"`
	DBID   uint32      `json:"db_id"`
	CMD    string      `json:"cmd"`
	Data   interface{} `json:"data"`
}

func printChange(op string, rel *pglogrepl.RelationMessage, tuple *pglogrepl.TupleData) {
	if rel == nil || tuple == nil {
		return
	}

	// Build a map of column name -> value
	rowData := make(map[string]string)
	for i, col := range tuple.Columns {
		if col.Data != nil {
			rowData[rel.Columns[i].Name] = string(col.Data)
		} else {
			rowData[rel.Columns[i].Name] = "NULL"
		}
	}

	// Create event struct
	dbevent := DBEvent{
		DBName: rel.RelationName,
		DBID:   rel.RelationID,
		CMD:    op,
		Data:   rowData,
	}

	// Log to console for debug
	fmt.Printf("CDC Event: %+v\n", dbevent)
}
