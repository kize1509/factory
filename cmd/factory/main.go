// Command factory runs the engine.
//
//	factory init            add factory.yml and ignore .factory/ in this repo
//	factory serve [flags]   start the engine and the web interface
//	factory test replay|results <run> [--json]   reproduce or read a recorded run (docs/TESTING.md)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"factory/internal/config"
	"factory/internal/events"
	"factory/internal/gateway"
	"factory/internal/runtime"
	"factory/internal/server"
	"factory/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: factory init | factory serve [-port 7777]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		initRepo()
	case "serve":
		serve(os.Args[2:])
	case "test":
		os.Exit(testCmd(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func initRepo() {
	if _, err := os.Stat("factory.yml"); err == nil {
		fmt.Println("factory.yml already exists")
	} else if err := os.WriteFile("factory.yml", []byte(config.Template), 0o644); err != nil {
		log.Fatal(err)
	} else {
		fmt.Println("wrote factory.yml")
	}
	data, _ := os.ReadFile(".gitignore")
	if !containsLine(string(data), ".factory/") {
		f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			f.WriteString("\n")
		}
		f.WriteString(".factory/\n")
		f.Close()
		fmt.Println("added .factory/ to .gitignore")
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 7777, "port for the API, the gateway and the web interface")
	root := fs.String("root", ".", "folder that holds web/ and pi-host/")
	fs.Parse(args)
	if abs, err := filepath.Abs(*root); err == nil {
		*root = abs
	}

	settings := config.LoadEngine()
	wire, err := gateway.FindWire(settings.Wire)
	if err != nil {
		log.Fatal(err)
	}
	// make sure the Central proxy is up; it's fine if it already is
	exec.Command("central", "proxy", "start", "--ensure-updated", "--caller", "agent").Run()

	home := config.Home()
	db, err := store.Open(filepath.Join(home, "factory.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	bus := events.NewBus(filepath.Join(home, "jobs"))
	bus.Persist, bus.Load = db.AddEvent, db.Events
	defer bus.Close()
	engine := fmt.Sprintf("http://127.0.0.1:%d", *port)
	model := settings.Model
	if model == "" {
		model = "anthropic/claude-sonnet-5-5"
	}
	s := &server.Server{
		Bus:     bus,
		Gateway: gateway.New(wire, bus),
		Runtimes: map[string]runtime.Runtime{
			"pi":          &runtime.Pi{HostDir: filepath.Join(*root, "pi-host"), AgentDir: filepath.Join(home, "pi-agent"), Engine: engine, Bus: bus},
			"claude-code": &runtime.Claude{Engine: engine, Bus: bus},
			"codex":       &runtime.Codex{Engine: engine, Bus: bus},
		},
		Web:     filepath.Join(*root, "web"),
		Model:   model,
		Root:    *root,
		Started: time.Now(),
		Store:   db,
	}
	s.LoadJobs()
	// prices: what's stored now, then a fresh sync on start and once a day
	s.ApplyPrices()
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			if source, n, err := s.SyncPrices(ctx); err != nil {
				log.Printf("prices: %v (keeping the stored ones)", err)
			} else {
				log.Printf("prices: %d models from %s", n, source)
			}
			cancel()
			time.Sleep(24 * time.Hour)
		}
	}()
	log.Printf("factory engine on %s (wire %s, default model %s)", engine, wire, model)
	log.Fatal(http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), s.Handler()))
}

func containsLine(text, line string) bool {
	for _, l := range splitLines(text) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
