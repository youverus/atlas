// Command atlas-gateway runs the IM gateway: chat platforms on one side, the
// same assembly kernel the CLI and the window drive on the other.
//
// It is a separate binary — not an `atlas` subcommand — for the same reason
// desktop/ is a separate module: it imports a third-party SDK, and the kernel
// module's dependency gate keeps that out of pi-go. One binary, two verbs:
//
//	atlas-gateway               run the gateway (foreground)
//	atlas-gateway approve CODE  admit the user that code belongs to
//
// Configuration lives in <atlas home>/channels.json (a template is written on
// first run), runtime state in <atlas home>/channels/.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"pi-go/channel"
	"pi-go/channel/feishu"
	"pi-go/internal/config"
)

const version = "0.1.0"

func main() {
	log.SetFlags(0)
	log.SetPrefix("atlas-gateway: ")

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "approve":
			runApprove(os.Args[2:])
			return
		case "-h", "--help", "help":
			usage()
			return
		case "-v", "--version", "version":
			fmt.Println("atlas-gateway " + version)
			return
		}
	}

	if err := runGateway(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Print(`atlas-gateway — atlas 的 IM 网关

用法：
  atlas-gateway [-config FILE] [-channels FILE] [-data DIR]
  atlas-gateway approve CODE
  atlas-gateway [-v]

配置在 <atlas home>/channels.json，运行状态在 <atlas home>/channels/。
`)
}

func runGateway(args []string) error {
	fs := flag.NewFlagSet("atlas-gateway", flag.ContinueOnError)
	configPath := fs.String("config", "", "内核用的 atlas 配置文件（默认 atlas home 下的 config.json）")
	channelsFlag := fs.String("channels", "", "channels.json 路径（默认 atlas home 下）")
	dataFlag := fs.String("data", "", "运行状态目录（默认 atlas home/channels）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	channelsPath := *channelsFlag
	if channelsPath == "" {
		var err error
		if channelsPath, err = channel.ChannelsPath(); err != nil {
			return err
		}
	}
	dataDir := *dataFlag
	if dataDir == "" {
		var err error
		if dataDir, err = channel.DataPath(); err != nil {
			return err
		}
	}
	if err := channel.WriteTemplate(channelsPath); err != nil {
		return err
	}

	sections, err := channel.LoadSections(channelsPath)
	if err != nil {
		return err
	}
	if sections == nil {
		// LoadSections answers nil only for a missing file; WriteTemplate
		// above means this cannot happen, but a template nobody edited is a
		// configuration that says nothing either.
		return fmt.Errorf("编辑 %s 填入 appId 与 appSecret 后重启", channelsPath)
	}
	empty := true
	for _, body := range sections {
		if len(body) > 0 {
			empty = false
		}
	}
	if empty {
		return fmt.Errorf("编辑 %s 填入 appId 与 appSecret 后重启", channelsPath)
	}

	allowlist, err := channel.OpenAllowlist(filepath.Join(dataDir, "allowlist.json"))
	if err != nil {
		return err
	}
	pairing, err := channel.OpenPairing(filepath.Join(dataDir, "pairing.json"))
	if err != nil {
		return err
	}
	store, err := channel.OpenBindings(filepath.Join(dataDir, "bindings.json"), "")
	if err != nil {
		return err
	}
	dedupe, err := channel.OpenDedupe(filepath.Join(dataDir, "dedupe.json"), 8192)
	if err != nil {
		return err
	}

	var adapters []channel.Adapter
	if raw, ok := sections["feishu"]; ok {
		cfg, err := feishu.Parse(raw)
		if err != nil {
			return err
		}
		adapter, err := feishu.New(cfg, allowlist, pairing, log.Default())
		if err != nil {
			return err
		}
		adapters = append(adapters, adapter)
	}
	if len(adapters) == 0 {
		return fmt.Errorf("%s 里没有任何平台的配置", channelsPath)
	}

	gateway, err := channel.New(channel.Options{
		Adapters: adapters,
		Store:    store,
		Dedupe:   dedupe,
	})
	if err != nil {
		return err
	}
	// The runner's approval questions route through the gateway, which owns
	// the adapters — the same inversion the window's approval POST uses.
	runner := channel.NewAssemblyRunner(*configPath, "atlas-gateway/"+version, store, gateway, log.Default())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("atlas-gateway %s 启动（配置 %s）", version, channelsPath)
	if err := gateway.Start(ctx, runner); err != nil {
		return err
	}
	return runner.Close()
}

// runApprove exchanges a pairing code for a user id in the runtime allowlist.
// It runs in its own process while the gateway keeps serving — the allowlist
// file is re-read by the adapter on the next message, so no restart.
func runApprove(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "用法：atlas-gateway approve <配对码>")
		os.Exit(2)
	}
	home, err := config.DefaultHome()
	if err != nil {
		log.Fatal(err)
	}
	dataDir := filepath.Join(home, "channels")
	pairing, err := channel.OpenPairing(filepath.Join(dataDir, "pairing.json"))
	if err != nil {
		log.Fatal(err)
	}
	adapter, userID, err := pairing.Approve(args[0])
	if err != nil {
		log.Fatal(err)
	}
	allowlist, err := channel.OpenAllowlist(filepath.Join(dataDir, "allowlist.json"))
	if err != nil {
		log.Fatal(err)
	}
	if err := allowlist.Add(adapter, userID); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("已批准 %s 用户 %s 加入白名单。让它再发一条消息即可开始对话。\n", adapter, userID)
}
