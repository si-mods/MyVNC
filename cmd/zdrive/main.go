package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	cmd := exec.Command(os.Args[1])
	cmd.Env = append(os.Environ(), "MYVNC_MCP_PASSWORD="+os.Getenv("PW"))
	cmd.Stderr = os.Stderr
	cl := mcp.NewClient(&mcp.Implementation{Name: "drive", Version: "0"}, nil)
	ctx := context.Background()
	sess, err := cl.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		panic(err)
	}
	defer sess.Close()
	tl, _ := sess.ListTools(ctx, nil)
	for _, t := range tl.Tools {
		fmt.Print(t.Name, " ")
	}
	fmt.Println()
	call := func(name string, args map[string]any, save string) {
		t0 := time.Now()
		r, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		d := time.Since(t0)
		if err != nil {
			fmt.Printf("%s ERR %v\n", name, err)
			return
		}
		for _, c := range r.Content {
			switch v := c.(type) {
			case *mcp.TextContent:
				fmt.Printf("%s (%v err=%v): %s\n", name, d.Round(time.Millisecond), r.IsError, v.Text)
			case *mcp.ImageContent:
				fmt.Printf("%s (%v): image %s %d bytes\n", name, d.Round(time.Millisecond), v.MIMEType, len(v.Data))
				if save != "" {
					os.WriteFile(save, v.Data, 0o644)
				}
			}
		}
	}
	var script []map[string]any
	json.Unmarshal([]byte(os.Args[2]), &script)
	for _, st := range script {
		if w, ok := st["sleep"].(float64); ok {
			time.Sleep(time.Duration(w * float64(time.Second)))
			continue
		}
		save, _ := st["save"].(string)
		args, _ := st["args"].(map[string]any)
		call(st["tool"].(string), args, save)
	}
}
