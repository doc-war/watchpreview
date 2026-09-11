package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// 防抖窗口固定为常量：构建工具批量写文件时合并成一次 reload。
// 这个值几乎不需要按场景调整，做成参数只会增加调用方的认知负担。
const debounceWindow = 200 * time.Millisecond

var defaultIgnoreDirs = []string{".git", "node_modules"}

type Watcher struct {
	root   string
	hub    *ReloadHub
	ignore []string
}

func NewWatcher(root string, hub *ReloadHub, ignore []string) *Watcher {
	return &Watcher{root: root, hub: hub, ignore: ignore}
}

func (w *Watcher) shouldSkipDir(name, fullPath string) bool {
	for _, d := range defaultIgnoreDirs {
		if name == d {
			return true
		}
	}

	if strings.HasPrefix(name, ".") {
		return true
	}

	for _, pattern := range w.ignore {
		if pattern != "" && strings.Contains(fullPath, pattern) {
			return true
		}
	}

	return false
}

func (w *Watcher) Run(ctx context.Context) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fw.Close()

	err = filepath.WalkDir(w.root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if path != w.root && w.shouldSkipDir(d.Name(), path) {
			return filepath.SkipDir
		}
		return fw.Add(path)
	})
	if err != nil {
		return err
	}

	var timer *time.Timer
	fire := make(chan struct{}, 1)

	resetTimer := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounceWindow, func() {
			select {
			case fire <- struct{}{}:
			default:
			}
		})
	}

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return nil

		case <-fire:
			w.hub.Reload()

		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "watchpreview: watcher error:", err)
			}

		case event, ok := <-fw.Events:
			if !ok {
				return nil
			}

			if event.Op&fsnotify.Create != 0 {
				if info, statErr := os.Stat(event.Name); statErr == nil && info.IsDir() {
					if !w.shouldSkipDir(filepath.Base(event.Name), event.Name) {
						_ = fw.Add(event.Name)
					}
					continue
				}
			}

			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				resetTimer()
			}
		}
	}
}
