package dsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"

	"github.com/qri-io/dag"
)

// HTTPClient is the request side of doing dsync over HTTP
type HTTPClient struct {
	URL string
}

// HTTPClient exists to satisfy the DaySyncable interface on the client side
// of a transfer
var _ DagSyncable = (*HTTPClient)(nil)

// NewReceiveSession initiates a session for pushing blocks to a remote.
// It sends a Manifest to a remote source over HTTP
func (rem *HTTPClient) NewReceiveSession(info *dag.Info, pinOnComplete bool, meta map[string]string) (sid string, diff *dag.Manifest, err error) {
	buf := &bytes.Buffer{}
	if err = json.NewEncoder(buf).Encode(info); err != nil {
		return
	}

	u, err := url.Parse(rem.URL)
	if err != nil {
		return
	}
	q := u.Query()
	q.Set("pin", fmt.Sprintf("%t", pinOnComplete))
	for key, val := range meta {
		q.Set(key, val)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("POST", u.String(), buf)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}

	if res.StatusCode != http.StatusOK {
		var msg string
		if data, err := ioutil.ReadAll(res.Body); err == nil {
			msg = string(data)
		}
		err = fmt.Errorf("remote response: %d %s", res.StatusCode, msg)
		return
	}

	sid = res.Header.Get("sid")
	diff = &dag.Manifest{}
	err = json.NewDecoder(res.Body).Decode(diff)

	return
}

// ReceiveBlock asks a remote to receive a block over HTTP
func (rem *HTTPClient) ReceiveBlock(sid, hash string, data []byte) ReceiveResponse {
	url := fmt.Sprintf("%s?sid=%s&hash=%s", rem.URL, sid, hash)
	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(data))
	if err != nil {
		return ReceiveResponse{
			Hash:   hash,
			Status: StatusErrored,
			Err:    err,
		}
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ReceiveResponse{
			Hash:   hash,
			Status: StatusErrored,
			Err:    err,
		}
	}

	if res.StatusCode != http.StatusOK {
		var msg string
		if data, err := ioutil.ReadAll(res.Body); err == nil {
			msg = string(data)
		}
		return ReceiveResponse{
			Hash:   hash,
			Status: StatusErrored,
			Err:    fmt.Errorf("remote error: %d %s", res.StatusCode, msg),
		}
	}
	return ReceiveResponse{
		Hash:   hash,
		Status: StatusOk,
	}
}

// GetDagInfo fetches a manifest from a remote source over HTTP
func (rem *HTTPClient) GetDagInfo(ctx context.Context, id string, meta map[string]string) (info *dag.Info, err error) {
	u, err := url.Parse(rem.URL)
	if err != nil {
		return
	}
	q := u.Query()
	q.Set("manifest", id)
	for key, val := range meta {
		q.Set(key, val)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		var msg string
		if data, err := ioutil.ReadAll(res.Body); err == nil {
			msg = string(data)
		}
		return nil, fmt.Errorf("remote error: %d %s", res.StatusCode, msg)
	}
	defer res.Body.Close()

	info = &dag.Info{}
	err = json.NewDecoder(res.Body).Decode(info)
	return
}

// GetBlock fetches a block from a remote source over HTTP
func (rem *HTTPClient) GetBlock(ctx context.Context, id string) (data []byte, err error) {
	url := fmt.Sprintf("%s?block=%s", rem.URL, id)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		var msg string
		if data, err := ioutil.ReadAll(res.Body); err == nil {
			msg = string(data)
		}
		return nil, fmt.Errorf("remote error: %d %s", res.StatusCode, msg)
	}
	defer res.Body.Close()

	return ioutil.ReadAll(res.Body)
}

// RemoveCID asks a remote to remove a CID
func (rem *HTTPClient) RemoveCID(ctx context.Context, id string, meta map[string]string) (err error) {
	u, err := url.Parse(rem.URL)
	if err != nil {
		return
	}
	q := u.Query()
	q.Set("cid", id)
	for key, val := range meta {
		q.Set(key, val)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("DELETE", u.String(), nil)
	if err != nil {
		return err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	if res.StatusCode != http.StatusOK {
		var msg string
		if data, err := ioutil.ReadAll(res.Body); err == nil {
			msg = string(data)
		}
		if msg == ErrRemoveNotSupported.Error() {
			return ErrRemoveNotSupported
		}
		return fmt.Errorf("remote: %d %s", res.StatusCode, msg)
	}

	return nil
}

// HTTPRemoteHandler exposes a Dsync remote over HTTP by exposing a HTTP handler
// that interlocks with methods exposed by HTTPClient
func HTTPRemoteHandler(ds *Dsync) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			info := &dag.Info{}
			if err := json.NewDecoder(r.Body).Decode(info); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(err.Error()))
				return
			}
			r.Body.Close()

			if info == nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("body must be a json dag info object"))
				return
			}

			pinOnComplete := r.FormValue("pin") == "true"
			meta := map[string]string{}
			for key := range r.URL.Query() {
				if key != "pin" {
					meta[key] = r.URL.Query().Get(key)
				}
			}

			sid, diff, err := ds.NewReceiveSession(info, pinOnComplete, meta)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(err.Error()))
				return
			}

			w.Header().Set("sid", sid)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(diff)

		case "PUT":
			data, err := ioutil.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(err.Error()))
				return
			}

			res := ds.ReceiveBlock(r.FormValue("sid"), r.FormValue("hash"), data)

			if res.Status == StatusErrored {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(res.Err.Error()))
			} else if res.Status == StatusRetry {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(res.Err.Error()))
			} else {
				w.WriteHeader(http.StatusOK)
			}
		case "GET":
			mfstID := r.FormValue("manifest")
			blockID := r.FormValue("block")
			if mfstID == "" && blockID == "" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("either manifest or block query params are required"))
			} else if mfstID != "" {

				meta := map[string]string{}
				for key := range r.URL.Query() {
					if key != "manifest" {
						meta[key] = r.URL.Query().Get(key)
					}
				}

				mfst, err := ds.GetDagInfo(r.Context(), mfstID, meta)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(err.Error()))
					return
				}

				data, err := json.Marshal(mfst)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(err.Error()))
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.Write(data)
			} else {
				data, err := ds.GetBlock(r.Context(), blockID)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(err.Error()))
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Write(data)
			}
		case "DELETE":
			cid := r.FormValue("cid")
			meta := map[string]string{}
			for key := range r.URL.Query() {
				if key != "cid" {
					meta[key] = r.URL.Query().Get(key)
				}
			}

			if err := ds.RemoveCID(r.Context(), cid, meta); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(err.Error()))
				return
			}

			w.WriteHeader(http.StatusOK)
		}
	}
}
