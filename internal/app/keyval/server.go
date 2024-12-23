package keyval

import (
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gofiber/fiber/v3"
	"github.com/syndtr/goleveldb/leveldb/util"
	"github.com/valyala/fasthttp"
)

type ListResponse struct {
	Keys     []string `json:"keys"`
	HasMore  bool     `json:"has_more"`
	NextPage string   `json:"next_page,omitempty"`
}

const (
	MAX_QUERY_LIMIT = 1000
)

func (k *KeyVal) QueryHandler(key []byte, c fiber.Ctx) {
	m := c.Queries()
	// operation is first query parameter (e.g. ?limit=10)
	_, unlinkedOpOk := m["unlinked"]
	start := m["start"]
	limit := 0
	qlimit := m["limit"]
	if qlimit != "" {
		nlimit, err := strconv.Atoi(qlimit)
		if err != nil {
			c.Status(fiber.StatusBadRequest)
			return
		}
		limit = nlimit
	}

	slice := util.BytesPrefix(key)
	if start != "" {
		slice.Start = []byte(start)
	}
	iter := k.db.NewIterator(slice, nil)
	defer iter.Release()
	keys := make([]string, 0)
	next := ""
	for iter.Next() {
		rec := toRecord(iter.Value())
		if (rec.Deleted != NO) ||
			(rec.Deleted != SOFT && unlinkedOpOk) {
			continue
		}
		if len(keys) > MAX_QUERY_LIMIT {
			c.Status(fiber.StatusRequestEntityTooLarge)
			return
		}
		keys = append(keys, string(iter.Key()))
		if limit > 0 && len(keys) > limit { // limit results returned
			next = string(iter.Key())
			keys = keys[:limit]
			break
		}
	}

	nextURI := fasthttp.AcquireURI()
	c.Request().URI().CopyTo(nextURI)
	nextPage := ""
	if next != "" {
		nextURI.QueryArgs().Set("start", next)
		nextPage = nextURI.String()
	} else {
		nextURI.QueryArgs().Del("start")
	}
	c.Status(fiber.StatusOK)
	c.Set("Content-Type", "application/json")
	c.JSON(ListResponse{
		NextPage: nextPage,
		HasMore:  next != "",
		Keys:     keys,
	})
	return
}

func (k *KeyVal) Delete(key []byte, unlink bool) int {
	// delete the key, first locally
	rec := k.GetRecord(key)
	if rec.Deleted == HARD || (unlink && rec.Deleted == SOFT) {
		return fiber.StatusNotFound
	}

	if !unlink && k.softDelete && rec.Deleted == NO {
		return fiber.StatusForbidden
	}

	// mark as deleted
	if err := k.PutRecord(key, Record{SOFT, rec.Hash}); err != nil {
		k.log.Error("failed to put record", "error", err)
		return fiber.StatusInternalServerError
	}

	if !unlink {
		if err := os.Remove(filepath.Join(k.volume, KeyToPath(key))); err != nil {
			k.log.Error("failed to delete file", "error", err)
			return fiber.StatusInternalServerError
		}

		// this is a hard delete in the database, aka nothing
		k.db.Delete(key, nil)
	}

	// 204, all good
	return fiber.StatusNoContent

}

func (k *KeyVal) Write(key []byte, value io.Reader, valueLen int64) int {
	// push to leveldb initially as deleted, and without a hash since we don't have it yet
	if err := k.PutRecord(key, Record{SOFT, ""}); err != nil {
		k.log.Error("failed to put record", "error", err)
		return fiber.StatusInternalServerError
	}

	// If the file already exists, overwrite it. Otherwise create it and write to it.
	fp := filepath.Join(k.volume, KeyToPath(key))
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		k.log.Error("failed to create directory", "error", err)
		return fiber.StatusInternalServerError
	}

	f, err := os.OpenFile(fp, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		k.log.Error("failed to open file", "error", err)
		return fiber.StatusInternalServerError
	}
	defer f.Close()

	h := md5.New()
	buf := make([]byte, 32*1024)
	if _, err := io.CopyBuffer(f, io.TeeReader(value, h), buf); err != nil {
		return fiber.StatusInternalServerError
	}
	hash := fmt.Sprintf("%x", h.Sum(nil))

	// push to leveldb as existing
	if err := k.PutRecord(key, Record{NO, hash}); err != nil {
		k.log.Error("failed to put record", "error", err)
		return fiber.StatusInternalServerError
	}

	// 201, all good
	return fiber.StatusCreated
}

func (k *KeyVal) ServeHTTP(c fiber.Ctx) error {
	url := c.Request().URI()
	method := c.Method()
	key := url.Path()
	m := c.Queries()

	// List query
	if string(c.Path()) == k.basePath && c.Method() == "GET" {
		k.QueryHandler(key, c)
		return nil
	}

	// Lock the key while a PUT or DELETE is in progress
	if method == "POST" || method == "PUT" || method == "DELETE" {
		if !k.LockKey(key) {
			// Retry later
			c.Status(fiber.StatusConflict)
			return nil
		}
		defer k.UnlockKey(key)
	}

	switch method {
	case "GET", "HEAD":
		rec := k.GetRecord(key)
		var fp string
		if len(rec.Hash) != 0 {
			// note that the hash is always of the whole file, not the content requested
			c.Set("Content-Md5", rec.Hash)
		}
		if rec.Deleted == SOFT || rec.Deleted == HARD {
			c.Set("Content-Length", "0")
			c.Status(fiber.StatusNotFound)
			return nil
		}

		// check if the file exists
		if _, err := os.Stat(filepath.Join(k.volume, KeyToPath(key))); err != nil {
			c.Set("Content-Length", "0")
			c.Status(fiber.StatusNotFound)
			return nil
		}

		c.Status(fiber.StatusOK)
		if method == "GET" {
			fp = filepath.Join(k.volume, KeyToPath(key))
			c.SendFile(fp)
		}

	case "PUT":
		// no empty values
		if c.Request().Header.ContentLength() == 0 {
			c.Status(411)
			return nil
		}

		status := k.Write(key, c.Request().BodyStream(), int64(c.Request().Header.ContentLength()))
		c.Status(status)

	case "DELETE":
		_, unlink := m["unlink"]
		status := k.Delete(key, unlink)
		c.Status(status)
	}

	return nil
}
