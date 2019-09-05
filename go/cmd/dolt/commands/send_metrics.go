package commands

import (
	"context"
	"log"
	"time"

	"github.com/liquidata-inc/dolt/go/libraries/doltcore/dbfactory"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/env"
	"github.com/liquidata-inc/dolt/go/libraries/events"
)

// SendMetricsCommand is the command used for sending metrics
const SendMetricsCommand = "send-metrics"

// var errMetricsDisabled = errors.New("metrics are currently disabled")

func flockAndFlush(ctx context.Context, dEnv *env.DoltEnv, egf *events.EventGrpcFlush) error {
	lck, err := dEnv.FS.LockWithTimeout(egf.LockPath, 100*time.Millisecond)
	if err != nil {
		return err
	}

	defer func() {
		err := lck.Unlock()
		if err != nil {
			log.Print(err)
		}
	}()

	err = egf.FlushEvents(ctx)
	if err != nil {
		// unlock should run?
		return err
	}

	return nil
}

// SendMetrics is the commandFunc used that flushes the events to the grpc service
func SendMetrics(ctx context.Context, commandStr string, args []string, dEnv *env.DoltEnv) int {
	disabled, err := events.AreMetricsDisabled(dEnv)
	if !disabled && err == nil {
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()

		root, err := dEnv.GetUserHomeDir()
		if err != nil {
			// log.Print(err)
			return 1
		}

		dolt := dbfactory.DoltDir

		egf := events.NewEventGrpcFlush(dEnv.FS, root, dolt, dEnv)

		err = flockAndFlush(ctx, dEnv, egf)
		if err != nil {
			return 2
		}

		return 0
	}

	if err != nil {
		// log.Print(err)
		return 1
	}

	// log.Print(errMetricsDisabled)
	return 0
}
