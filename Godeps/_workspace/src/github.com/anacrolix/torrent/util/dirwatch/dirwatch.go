// Package dirwatch provides filesystem-notification based tracking of torrent
// info files and magnet URIs in a directory.
package dirwatch

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/anacrolix/missinggo"
	"github.com/go-fsnotify/fsnotify"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

type Change uint

const (
	Added Change = iota
	Removed
)

type Event struct {
	MagnetURI string
	Change
	TorrentFilePath string
	InfoHash        torrent.InfoHash
}

type entity struct {
	torrent.InfoHash
	MagnetURI       string
	TorrentFilePath string
}

type Instance struct {
	w        *fsnotify.Watcher
	dirName  string
	Events   chan Event
	dirState map[torrent.InfoHash]entity
}

func (me *Instance) Close() {
	me.w.Close()
}

func (me *Instance) handleEvents() {
	defer close(me.Events)
	for e := range me.w.Events {
		log.Printf("event: %s", e)
		if e.Op == fsnotify.Write {
			// TODO: Special treatment as an existing torrent may have changed.
		} else {
			me.refresh()
		}
	}
}

func (me *Instance) handleErrors() {
	for err := range me.w.Errors {
		log.Printf("error in torrent directory watcher: %s", err)
	}
}

func torrentFileInfoHash(fileName string) (ih torrent.InfoHash, ok bool) {
	mi, _ := metainfo.LoadFromFile(fileName)
	if mi == nil {
		return
	}
	missinggo.CopyExact(ih[:], mi.Info.Hash)
	ok = true
	return
}

func scanDir(dirName string) (ee map[torrent.InfoHash]entity) {
	d, err := os.Open(dirName)
	if err != nil {
		log.Print(err)
		return
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		log.Print(err)
		return
	}
	ee = make(map[torrent.InfoHash]entity, len(names))
	addEntity := func(e entity) {
		e0, ok := ee[e.InfoHash]
		if ok {
			if e0.MagnetURI == "" || len(e.MagnetURI) < len(e0.MagnetURI) {
				return
			}
		}
		ee[e.InfoHash] = e
	}
	for _, n := range names {
		fullName := filepath.Join(dirName, n)
		switch filepath.Ext(n) {
		case ".torrent":
			ih, ok := torrentFileInfoHash(fullName)
			if !ok {
				break
			}
			e := entity{
				TorrentFilePath: fullName,
			}
			missinggo.CopyExact(&e.InfoHash, ih)
			addEntity(e)
		case ".magnet":
			uris, err := magnetFileURIs(fullName)
			if err != nil {
				log.Print(err)
				break
			}
			for _, uri := range uris {
				m, err := torrent.ParseMagnetURI(uri)
				if err != nil {
					log.Printf("error parsing %q in file %q: %s", uri, fullName, err)
					continue
				}
				addEntity(entity{
					InfoHash:  m.InfoHash,
					MagnetURI: uri,
				})
			}
		}
	}
	return
}

func magnetFileURIs(name string) (uris []string, err error) {
	f, err := os.Open(name)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Split(bufio.ScanWords)
	for scanner.Scan() {
		// Allow magnet URIs to be "commented" out.
		if strings.HasPrefix(scanner.Text(), "#") {
			continue
		}
		uris = append(uris, scanner.Text())
	}
	err = scanner.Err()
	return
}

func (me *Instance) torrentRemoved(ih torrent.InfoHash) {
	me.Events <- Event{
		InfoHash: ih,
		Change:   Removed,
	}
}

func (me *Instance) torrentAdded(e entity) {
	me.Events <- Event{
		InfoHash:        e.InfoHash,
		Change:          Added,
		MagnetURI:       e.MagnetURI,
		TorrentFilePath: e.TorrentFilePath,
	}
}

func (me *Instance) refresh() {
	_new := scanDir(me.dirName)
	old := me.dirState
	for ih, _ := range old {
		_, ok := _new[ih]
		if !ok {
			me.torrentRemoved(ih)
		}
	}
	for ih, newE := range _new {
		oldE, ok := old[ih]
		if ok {
			if newE == oldE {
				continue
			}
			me.torrentRemoved(ih)
		}
		me.torrentAdded(newE)
	}
	me.dirState = _new
}

func New(dirName string) (i *Instance, err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	err = w.Add(dirName)
	if err != nil {
		w.Close()
		return
	}
	i = &Instance{
		w:        w,
		dirName:  dirName,
		Events:   make(chan Event),
		dirState: make(map[torrent.InfoHash]entity, 0),
	}
	go func() {
		i.refresh()
		go i.handleEvents()
		go i.handleErrors()
	}()
	return
}
