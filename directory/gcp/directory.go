// Package directory implements the interface upspin.Directory for talking to an HTTP server.
package directory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"upspin.googlesource.com/upspin.git/cloud/netutil"
	"upspin.googlesource.com/upspin.git/cloud/netutil/parser"
	"upspin.googlesource.com/upspin.git/upspin"
)

// Directory is an implementation of upspin.Directory that uses GCP to store its data.
type Directory struct {
	serverURL    string
	storeService upspin.Store
	client       HTTPClientInterface
}

// Guarantee we implement the interface
var _ upspin.Directory = (*Directory)(nil)

// HTTPClientInterface is a minimal HTTP client interface. An instance of
// http.Client suffices.
// TODO(edpin): this should move somewhere, probably cloud/netutil.
type HTTPClientInterface interface {
	Do(req *http.Request) (resp *http.Response, err error)
}

// New returns a concrete implementation of Directory, pointing to a server at a given URL and port.
func New(serverURL string, storeService upspin.Store, client HTTPClientInterface) *Directory {
	return &Directory{
		serverURL:    serverURL,
		storeService: storeService,
		client:       client,
	}
}

func (d *Directory) Lookup(name upspin.PathName) (*upspin.DirEntry, error) {
	const op = "Lookup"
	// Prepare a get request to the server
	req, err := http.NewRequest(netutil.Get, fmt.Sprintf("%s/get?pathname=%s", d.serverURL, name), nil)
	if err != nil {
		return nil, newError(op, name, err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, newError(op, name, err)
	}
	// Check the response
	if resp.StatusCode != http.StatusOK {
		return nil, newError(op, name,
			errors.New(fmt.Sprintf("server error: %v", resp.StatusCode)))
	}
	// Check the payload
	defer resp.Body.Close()
	// TODO(edpin): maybe add a limit here to the size of bytes we
	// read and return?
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Check the content type
	answerType := resp.Header.Get(netutil.ContentType)
	if !strings.HasPrefix(answerType, "application/json") {
		return nil, newError(op, name, errors.New(fmt.Sprintf("Invalid response format: %v", answerType)))
	}
	// Interpret the JSON returned
	dirEntry, err := parser.DirEntryResponse(body)
	if err != nil {
		return nil, err
	}
	return dirEntry, nil
}

func (d *Directory) Put(name upspin.PathName, data, packdata []byte) (upspin.Location, error) {
	var zeroLoc upspin.Location
	const op = "Put"

	// First, store the data itself, to find a location.
	ref := upspin.Reference{
		Key:     "", // The server will compute one. But we should request exactly what we expect when we have crypto here
		Packing: upspin.EEp256Pack,
	}
	loc, err := d.storeService.Put(ref, data)
	if err != nil {
		return zeroLoc, err
	}
	// We now have a final Location in loc. We now create a
	// directory entry for this Location.  From here on, if an
	// error occurs, we'll have a dangling block. We could delete
	// it, but we can always do fsck-style operations to find them
	// later.
	dirEntry := upspin.DirEntry{
		Name: name,
		Metadata: upspin.Metadata{
			IsDir:    false,
			Sequence: 0, // TODO: server does not increment currently
			PackData: packdata,
		},
	}

	// Encode dirEntry as JSON
	dirEntryJSON, err := json.Marshal(dirEntry)
	if err != nil {
		return zeroLoc, newError(op, name, err)
	}

	// Prepare a put request to the server
	req, err := http.NewRequest(netutil.Get, fmt.Sprintf("%s/put", d.serverURL), bytes.NewBuffer(dirEntryJSON))
	if err != nil {
		return zeroLoc, newError(op, name, err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return zeroLoc, newError(op, name, err)
	}

	// Get response.
	if resp.StatusCode != http.StatusOK {
		return zeroLoc, newError(op, name, errors.New(fmt.Sprintf("server error: %v", resp.StatusCode)))
	}

	// Read the body of the response
	defer resp.Body.Close()
	// TODO(edpin): maybe add a limit here to the size of bytes we return?
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return zeroLoc, newError(op, name, err)
	}
	err = parser.ErrorResponse(respBody)
	if err != nil {
		return zeroLoc, newError(op, name, err)
	}

	return loc, nil
}

func (d *Directory) MakeDirectory(dirName upspin.PathName) (upspin.Location, error) {
	var zeroLoc upspin.Location // The zero location
	const op = "MakeDirectory"

	// Prepares a request to put dirName to the server
	dirEntry := upspin.DirEntry{
		Name: dirName,
		Metadata: upspin.Metadata{
			IsDir:    true,
			Sequence: 0, // don't care?
			PackData: nil,
		},
	}
	body, err := json.Marshal(dirEntry)
	if err != nil {
		return zeroLoc, newError(op, dirName, err)
	}
	data := bytes.NewBuffer(body)
	req, err := http.NewRequest(netutil.Post, fmt.Sprintf("%s/put", d.serverURL), data)
	if err != nil {
		return zeroLoc, newError(op, dirName, err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return zeroLoc, newError(op, dirName, err)
	}

	// Check the response
	if resp.StatusCode != http.StatusOK {
		return zeroLoc, newError(op, dirName, errors.New(fmt.Sprintf("server error: %v", resp.StatusCode)))
	}

	// Read the body of the response
	defer resp.Body.Close()
	// TODO(edpin): maybe add a limit here to the size of bytes we return?
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return zeroLoc, newError(op, dirName, err)
	}
	loc, err := parser.LocationResponse(respBody)
	if err != nil {
		return zeroLoc, newError(op, dirName, err)
	}
	if loc == nil {
		return zeroLoc, newError(op, dirName, errors.New("server returned null Location"))
	}

	return *loc, nil
}

func (d *Directory) Glob(pattern string) ([]*upspin.DirEntry, error) {
	return nil, newError("Glob", "", errors.New("Glob unimplemented"))
}

func (d *Directory) Dial(upspin.ClientContext, upspin.Endpoint) (interface{}, error) {
	return nil, newError("Dial", "", errors.New("Dial unimplemented"))
}

func (d *Directory) ServerUserName() string {
	return "Not sure what to return here yet. Next CL will fix it."
}

func newError(op string, path upspin.PathName, err error) *os.PathError {
	return &os.PathError{
		Op:   op,
		Path: string(path),
		Err:  err,
	}
}