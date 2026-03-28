//go:build ignore

// jniproxy_scan demonstrates BLE scanning using the jni-proxy backend.
//
// The jni-proxy backend communicates with an Android device over gRPC.
// A jniservice app must be running on the device, exposing the JNI
// proxy service. The Go client runs anywhere (Linux, macOS, etc.)
// and connects to the device's gRPC endpoint.
//
// Prerequisites:
//   - jniservice running on the Android device (see AndroidGoLab/jni-proxy)
//   - Device reachable over the network (or via adb forward tcp:50051 tcp:50051)
//   - Client TLS certificate/key pair (jniservice uses mTLS by default)
//
// Build and run (runs on the host, not on the device):
//
//	go build -o /tmp/jniproxy_scan examples/android/jniproxy_scan.go
//	/tmp/jniproxy_scan -addr localhost:50051 -cert client.crt -key client.key
//	/tmp/jniproxy_scan -addr localhost:50051 -plaintext  # if jniservice started without TLS
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/facebookincubator/go-belt/tool/logger/implementation/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/android"
	"github.com/xaionaro-go/gatt/android/jniproxy"
)

func main() {
	addr := flag.String("addr", "localhost:50051", "jniservice gRPC address")
	cert := flag.String("cert", "", "client TLS certificate (for mTLS)")
	key := flag.String("key", "", "client TLS private key (for mTLS)")
	plaintext := flag.Bool("plaintext", false, "use plaintext (no TLS)")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ctx = logger.CtxWithLogger(ctx, logrus.Default())

	var transportCreds grpc.DialOption
	switch {
	case *cert != "" && *key != "":
		clientCert, err := tls.LoadX509KeyPair(*cert, *key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loading client cert: %v\n", err)
			os.Exit(1)
		}
		transportCreds = grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			Certificates:       []tls.Certificate{clientCert},
			InsecureSkipVerify: true,
		}))
	case *plaintext:
		transportCreds = grpc.WithTransportCredentials(insecure.NewCredentials())
	default:
		// TLS without client certificate — works if jniservice
		// is configured without mTLS requirement.
		transportCreds = grpc.WithTransportCredentials(credentials.NewTLS(
			&tls.Config{InsecureSkipVerify: true}))
	}

	conn, err := grpc.NewClient(*addr, transportCreds)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grpc dial: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	dev, err := android.NewDevice(ctx, android.BackendJNIProxy,
		jniproxy.WithGRPCConn(conn),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewDevice: %v\n", err)
		os.Exit(1)
	}

	dev.Handle(ctx,
		gatt.PeripheralDiscovered(func(
			ctx context.Context,
			p gatt.Peripheral,
			a *gatt.Advertisement,
			rssi int,
		) {
			fmt.Printf("discovered %s %q rssi=%d\n", p.ID(), a.LocalName, rssi)
		}),
	)

	if err := dev.Start(ctx, func(ctx context.Context, d gatt.Device, s gatt.State) {
		fmt.Printf("state: %s\n", s)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Start: %v\n", err)
		os.Exit(1)
	}

	if err := dev.Scan(ctx, nil, false); err != nil {
		fmt.Fprintf(os.Stderr, "Scan: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("scanning... (ctrl-c to stop)")

	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
	}

	_ = dev.StopScanning()
	_ = dev.Stop()
}
