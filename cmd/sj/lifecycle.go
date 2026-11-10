package main

import (
	"context"
	"fmt"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

const managerInterface = "org.freedesktop.systemd1.Manager"
const managerPath dbus.ObjectPath = "/org/freedesktop/systemd1"

func timerProperties(label string, delay time.Duration, at string) ([]systemd.Property, error) {
	if _, err := timerDelay(delay, at); err != nil {
		return nil, err
	}
	properties := []systemd.Property{
		systemd.PropDescription(label), prop("CollectMode", "inactive"),
		prop("RemainAfterElapse", false),
		prop("AccuracyUSec", uint64(1000)),
	}
	if delay > 0 {
		properties = append(properties, prop("TimersMonotonic", []struct {
			Base string
			Usec uint64
		}{{"OnActiveSec", max(1, uint64(delay/time.Microsecond))}}))
	} else {
		when, err := parseAtTime(at)
		if err != nil {
			return nil, err
		}
		properties = append(properties, prop("TimersCalendar", []struct {
			Base string
			Spec string
		}{{"OnCalendar", when.UTC().Format("2006-01-02 15:04:05.999999 UTC")}}))
	}
	return properties, nil
}

func startTransientUnit(name string, properties []systemd.Property, aux []systemd.PropertyCollection) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return startTransientUnitContext(ctx, name, properties, aux)
}

// Subscribe before submitting the request: JobRemoved can arrive before the
// method reply, particularly for very short commands and immediate failures.
// A private connection owns the subscription and releases it on every exit.
func startTransientUnitContext(ctx context.Context, name string, properties []systemd.Property, aux []systemd.PropertyCollection) error {
	bus, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return err
	}
	defer bus.Close()
	signals := make(chan *dbus.Signal, 16)
	bus.Signal(signals)
	defer bus.RemoveSignal(signals)
	if err := bus.AddMatchSignalContext(ctx,
		dbus.WithMatchSender("org.freedesktop.systemd1"),
		dbus.WithMatchObjectPath(managerPath),
		dbus.WithMatchInterface(managerInterface),
		dbus.WithMatchMember("JobRemoved"), dbus.WithMatchArg(2, name),
	); err != nil {
		return err
	}
	manager := bus.Object("org.freedesktop.systemd1", managerPath)
	if err := manager.CallWithContext(ctx, managerInterface+".Subscribe", 0).Err; err != nil {
		return err
	}
	var job dbus.ObjectPath
	if err := manager.CallWithContext(ctx, managerInterface+".StartTransientUnit", 0,
		name, "fail", properties, aux).Store(&job); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s startup (unit may still start): %w", name, ctx.Err())
		case signal, ok := <-signals:
			if !ok {
				if err := ctx.Err(); err != nil {
					return fmt.Errorf("waiting for %s startup (unit may still start): %w", name, err)
				}
				return fmt.Errorf("D-Bus connection closed while starting %s", name)
			}
			var id uint32
			var path dbus.ObjectPath
			var unit, result string
			if signal == nil || signal.Name != managerInterface+".JobRemoved" {
				continue
			}
			if err := dbus.Store(signal.Body, &id, &path, &unit, &result); err != nil {
				return fmt.Errorf("invalid JobRemoved signal: %w", err)
			}
			if path != job || unit != name {
				continue
			}
			if result != "done" {
				return fmt.Errorf("starting %s: systemd job result %s", name, result)
			}
			return nil
		}
	}
}
