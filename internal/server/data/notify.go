package data

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v4"
	pgxstdlib "github.com/jackc/pgx/v4/stdlib"

	"github.com/infrahq/infra/internal/logging"
	"github.com/infrahq/infra/uid"
)

type Listener struct {
	sqlDB   *sql.DB
	pgxConn *pgx.Conn
}

// WaitForNotification blocks until the listener receivers a notification on
// one of the channels, or until the context is cancelled.
// Returns the notification on success, or an error on failure or timeout.
func (l *Listener) WaitForNotification(ctx context.Context) error {
	_, err := l.pgxConn.WaitForNotification(ctx)
	return err
}

func (l *Listener) Release(ctx context.Context) error {
	var errs []error
	logging.Debugf("unlisten *")
	if _, err := l.pgxConn.Exec(ctx, `UNLISTEN *`); err != nil {
		errs = append(errs, err)
	}
	if err := pgxstdlib.ReleaseConn(l.sqlDB, l.pgxConn); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to unlisten to postgres channels: %v", errs)
	}
	return nil
}

type ListenChannelDescriptor interface {
	Channel() string
}

// ListenForNotify starts listening for notification on one or more
// postgres channels for notifications that a grant has changed. The channels to
// listen on are determined by opts. Use Listener.WaitForNotification to block
// and receive notifications.
//
// If error is nil the caller must call Listener.Release to return the database
// connection to the pool.
func ListenForNotify(ctx context.Context, db *DB, descriptors ...ListenChannelDescriptor) (*Listener, error) {
	sqlDB := db.SQLdb()
	pgxConn, err := pgxstdlib.AcquireConn(sqlDB)
	if err != nil {
		return nil, err
	}

	listener := &Listener{sqlDB: sqlDB, pgxConn: pgxConn}

	for _, descriptor := range descriptors {
		_, err = pgxConn.Exec(ctx, "SELECT listen_on_chan($1)", descriptor.Channel())
		if err != nil {
			if err := pgxstdlib.ReleaseConn(sqlDB, pgxConn); err != nil {
				logging.L.Warn().Err(err).Msgf("release pgx conn")
			}
			return nil, err
		}
	}
	return listener, nil
}

type ListenChannelGrantsByDestination struct {
	OrgID         uid.ID
	DestinationID uid.ID
}

func (l ListenChannelGrantsByDestination) Channel() string {
	return fmt.Sprintf("grantsByDest.%v.%v", l.OrgID, l.DestinationID)
}

type ListenChannelDestinationCredentialsByDestinationID struct {
	OrgID         uid.ID
	DestinationID uid.ID
}

func (l ListenChannelDestinationCredentialsByDestinationID) Channel() string {
	return fmt.Sprintf("dcredReq.%v.%v", l.OrgID, l.DestinationID)
}

type ListenChannelDestinationCredentialsByID struct {
	OrgID                    uid.ID
	DestinationCredentialsID uid.ID
}

func (l ListenChannelDestinationCredentialsByID) Channel() string {
	return fmt.Sprintf("dcredResp.%v.%v", l.OrgID, l.DestinationCredentialsID)
}

type ListenChannelGroupMembership struct {
	OrgID   uid.ID
	GroupID uid.ID
}

func (l ListenChannelGroupMembership) Channel() string {
	return fmt.Sprintf("group_membership.%v.%v", l.OrgID, l.GroupID)
}
