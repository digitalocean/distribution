package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"reflect"
	"testing"

	"github.com/docker/distribution/api/v2"
	"github.com/docker/distribution/configuration"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/manifest"
	_ "github.com/docker/distribution/storagedriver/inmemory"
	"github.com/docker/distribution/testutil"
	"github.com/docker/libtrust"
	"github.com/gorilla/handlers"
)

// TestCheckAPI hits the base endpoint (/v2/) ensures we return the specified
// 200 OK response.
func TestCheckAPI(t *testing.T) {
	config := configuration.Configuration{
		Storage: configuration.Storage{
			"inmemory": configuration.Parameters{},
		},
	}

	app := NewApp(config)
	server := httptest.NewServer(handlers.CombinedLoggingHandler(os.Stderr, app))
	builder, err := v2.NewURLBuilderFromString(server.URL)

	if err != nil {
		t.Fatalf("error creating url builder: %v", err)
	}

	baseURL, err := builder.BuildBaseURL()
	if err != nil {
		t.Fatalf("unexpected error building base url: %v", err)
	}

	resp, err := http.Get(baseURL)
	if err != nil {
		t.Fatalf("unexpected error issuing request: %v", err)
	}
	defer resp.Body.Close()

	checkResponse(t, "issuing api base check", resp, http.StatusOK)
	checkHeaders(t, resp, http.Header{
		"Content-Type":   []string{"application/json; charset=utf-8"},
		"Content-Length": []string{"2"},
	})

	p, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("unexpected error reading response body: %v", err)
	}

	if string(p) != "{}" {
		t.Fatalf("unexpected response body: %v", string(p))
	}
}

// TestLayerAPI conducts a full of the of the layer api.
func TestLayerAPI(t *testing.T) {
	// TODO(stevvooe): This test code is complete junk but it should cover the
	// complete flow. This must be broken down and checked against the
	// specification *before* we submit the final to docker core.

	config := configuration.Configuration{
		Storage: configuration.Storage{
			"inmemory": configuration.Parameters{},
		},
	}

	app := NewApp(config)
	server := httptest.NewServer(handlers.CombinedLoggingHandler(os.Stderr, app))
	builder, err := v2.NewURLBuilderFromString(server.URL)

	if err != nil {
		t.Fatalf("error creating url builder: %v", err)
	}

	imageName := "foo/bar"
	// "build" our layer file
	layerFile, tarSumStr, err := testutil.CreateRandomTarFile()
	if err != nil {
		t.Fatalf("error creating random layer file: %v", err)
	}

	layerDigest := digest.Digest(tarSumStr)

	// -----------------------------------
	// Test fetch for non-existent content
	layerURL, err := builder.BuildBlobURL(imageName, layerDigest)
	if err != nil {
		t.Fatalf("error building url: %v", err)
	}

	resp, err := http.Get(layerURL)
	if err != nil {
		t.Fatalf("unexpected error fetching non-existent layer: %v", err)
	}

	checkResponse(t, "fetching non-existent content", resp, http.StatusNotFound)

	// ------------------------------------------
	// Test head request for non-existent content
	resp, err = http.Head(layerURL)
	if err != nil {
		t.Fatalf("unexpected error checking head on non-existent layer: %v", err)
	}

	checkResponse(t, "checking head on non-existent layer", resp, http.StatusNotFound)

	// ------------------------------------------
	// Start an upload and cancel
	uploadURLBase := startPushLayer(t, builder, imageName)

	req, err := http.NewRequest("DELETE", uploadURLBase, nil)
	if err != nil {
		t.Fatalf("unexpected error creating delete request: %v", err)
	}

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error sending delete request: %v", err)
	}

	checkResponse(t, "deleting upload", resp, http.StatusNoContent)

	// A status check should result in 404
	resp, err = http.Get(uploadURLBase)
	if err != nil {
		t.Fatalf("unexpected error getting upload status: %v", err)
	}
	checkResponse(t, "status of deleted upload", resp, http.StatusNotFound)

	// -----------------------------------------
	// Do layer push with an empty body and different digest
	uploadURLBase = startPushLayer(t, builder, imageName)
	resp, err = doPushLayer(t, builder, imageName, layerDigest, uploadURLBase, bytes.NewReader([]byte{}))
	if err != nil {
		t.Fatalf("unexpected error doing bad layer push: %v", err)
	}

	checkResponse(t, "bad layer push", resp, http.StatusBadRequest)
	checkBodyHasErrorCodes(t, "bad layer push", resp, v2.ErrorCodeDigestInvalid)

	// -----------------------------------------
	// Do layer push with an empty body and correct digest
	zeroDigest, err := digest.FromTarArchive(bytes.NewReader([]byte{}))
	if err != nil {
		t.Fatalf("unexpected error digesting empty buffer: %v", err)
	}

	uploadURLBase = startPushLayer(t, builder, imageName)
	pushLayer(t, builder, imageName, zeroDigest, uploadURLBase, bytes.NewReader([]byte{}))

	// -----------------------------------------
	// Do layer push with an empty body and correct digest

	// This is a valid but empty tarfile!
	emptyTar := bytes.Repeat([]byte("\x00"), 1024)
	emptyDigest, err := digest.FromTarArchive(bytes.NewReader(emptyTar))
	if err != nil {
		t.Fatalf("unexpected error digesting empty tar: %v", err)
	}

	uploadURLBase = startPushLayer(t, builder, imageName)
	pushLayer(t, builder, imageName, emptyDigest, uploadURLBase, bytes.NewReader(emptyTar))

	// ------------------------------------------
	// Now, actually do successful upload.
	layerLength, _ := layerFile.Seek(0, os.SEEK_END)
	layerFile.Seek(0, os.SEEK_SET)

	uploadURLBase = startPushLayer(t, builder, imageName)
	pushLayer(t, builder, imageName, layerDigest, uploadURLBase, layerFile)

	// ------------------------
	// Use a head request to see if the layer exists.
	resp, err = http.Head(layerURL)
	if err != nil {
		t.Fatalf("unexpected error checking head on existing layer: %v", err)
	}

	checkResponse(t, "checking head on existing layer", resp, http.StatusOK)
	checkHeaders(t, resp, http.Header{
		"Content-Length": []string{fmt.Sprint(layerLength)},
	})

	// ----------------
	// Fetch the layer!
	resp, err = http.Get(layerURL)
	if err != nil {
		t.Fatalf("unexpected error fetching layer: %v", err)
	}

	checkResponse(t, "fetching layer", resp, http.StatusOK)
	checkHeaders(t, resp, http.Header{
		"Content-Length": []string{fmt.Sprint(layerLength)},
	})

	// Verify the body
	verifier := digest.NewDigestVerifier(layerDigest)
	io.Copy(verifier, resp.Body)

	if !verifier.Verified() {
		t.Fatalf("response body did not pass verification")
	}

	// Missing tests:
	// 	- Upload the same tarsum file under and different repository and
	//       ensure the content remains uncorrupted.
}

func TestManifestAPI(t *testing.T) {
	pk, err := libtrust.GenerateECP256PrivateKey()
	if err != nil {
		t.Fatalf("unexpected error generating private key: %v", err)
	}

	config := configuration.Configuration{
		Storage: configuration.Storage{
			"inmemory": configuration.Parameters{},
		},
	}

	app := NewApp(config)
	server := httptest.NewServer(handlers.CombinedLoggingHandler(os.Stderr, app))
	builder, err := v2.NewURLBuilderFromString(server.URL)
	if err != nil {
		t.Fatalf("unexpected error creating url builder: %v", err)
	}

	imageName := "foo/bar"
	tag := "thetag"

	manifestURL, err := builder.BuildManifestURL(imageName, tag)
	if err != nil {
		t.Fatalf("unexpected error getting manifest url: %v", err)
	}

	// -----------------------------
	// Attempt to fetch the manifest
	resp, err := http.Get(manifestURL)
	if err != nil {
		t.Fatalf("unexpected error getting manifest: %v", err)
	}
	defer resp.Body.Close()

	checkResponse(t, "getting non-existent manifest", resp, http.StatusNotFound)
	checkBodyHasErrorCodes(t, "getting non-existent manifest", resp, v2.ErrorCodeManifestUnknown)

	tagsURL, err := builder.BuildTagsURL(imageName)
	if err != nil {
		t.Fatalf("unexpected error building tags url: %v", err)
	}

	resp, err = http.Get(tagsURL)
	if err != nil {
		t.Fatalf("unexpected error getting unknown tags: %v", err)
	}
	defer resp.Body.Close()

	// Check that we get an unknown repository error when asking for tags
	checkResponse(t, "getting unknown manifest tags", resp, http.StatusNotFound)
	checkBodyHasErrorCodes(t, "getting unknown manifest tags", resp, v2.ErrorCodeNameUnknown)

	// --------------------------------
	// Attempt to push unsigned manifest with missing layers
	unsignedManifest := &manifest.Manifest{
		Name: imageName,
		Tag:  tag,
		FSLayers: []manifest.FSLayer{
			{
				BlobSum: "asdf",
			},
			{
				BlobSum: "qwer",
			},
		},
	}

	resp = putManifest(t, "putting unsigned manifest", manifestURL, unsignedManifest)
	defer resp.Body.Close()
	checkResponse(t, "posting unsigned manifest", resp, http.StatusBadRequest)
	_, p, counts := checkBodyHasErrorCodes(t, "getting unknown manifest tags", resp,
		v2.ErrorCodeManifestUnverified, v2.ErrorCodeBlobUnknown, v2.ErrorCodeDigestInvalid)

	expectedCounts := map[v2.ErrorCode]int{
		v2.ErrorCodeManifestUnverified: 1,
		v2.ErrorCodeBlobUnknown:        2,
		v2.ErrorCodeDigestInvalid:      2,
	}

	if !reflect.DeepEqual(counts, expectedCounts) {
		t.Fatalf("unexpected number of error codes encountered: %v\n!=\n%v\n---\n%s", counts, expectedCounts, string(p))
	}

	// TODO(stevvooe): Add a test case where we take a mostly valid registry,
	// tamper with the content and ensure that we get a unverified manifest
	// error.

	// Push 2 random layers
	expectedLayers := make(map[digest.Digest]io.ReadSeeker)

	for i := range unsignedManifest.FSLayers {
		rs, dgstStr, err := testutil.CreateRandomTarFile()

		if err != nil {
			t.Fatalf("error creating random layer %d: %v", i, err)
		}
		dgst := digest.Digest(dgstStr)

		expectedLayers[dgst] = rs
		unsignedManifest.FSLayers[i].BlobSum = dgst

		uploadURLBase := startPushLayer(t, builder, imageName)
		pushLayer(t, builder, imageName, dgst, uploadURLBase, rs)
	}

	// -------------------
	// Push the signed manifest with all layers pushed.
	signedManifest, err := manifest.Sign(unsignedManifest, pk)
	if err != nil {
		t.Fatalf("unexpected error signing manifest: %v", err)
	}

	resp = putManifest(t, "putting signed manifest", manifestURL, signedManifest)

	checkResponse(t, "putting signed manifest", resp, http.StatusOK)

	resp, err = http.Get(manifestURL)
	if err != nil {
		t.Fatalf("unexpected error fetching manifest: %v", err)
	}
	defer resp.Body.Close()

	checkResponse(t, "fetching uploaded manifest", resp, http.StatusOK)

	var fetchedManifest manifest.SignedManifest
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&fetchedManifest); err != nil {
		t.Fatalf("error decoding fetched manifest: %v", err)
	}

	if !bytes.Equal(fetchedManifest.Raw, signedManifest.Raw) {
		t.Fatalf("manifests do not match")
	}

	// Ensure that the tag is listed.
	resp, err = http.Get(tagsURL)
	if err != nil {
		t.Fatalf("unexpected error getting unknown tags: %v", err)
	}
	defer resp.Body.Close()

	// Check that we get an unknown repository error when asking for tags
	checkResponse(t, "getting unknown manifest tags", resp, http.StatusOK)
	dec = json.NewDecoder(resp.Body)

	var tagsResponse tagsAPIResponse

	if err := dec.Decode(&tagsResponse); err != nil {
		t.Fatalf("unexpected error decoding error response: %v", err)
	}

	if tagsResponse.Name != imageName {
		t.Fatalf("tags name should match image name: %v != %v", tagsResponse.Name, imageName)
	}

	if len(tagsResponse.Tags) != 1 {
		t.Fatalf("expected some tags in response: %v", tagsResponse.Tags)
	}

	if tagsResponse.Tags[0] != tag {
		t.Fatalf("tag not as expected: %q != %q", tagsResponse.Tags[0], tag)
	}
}

func putManifest(t *testing.T, msg, url string, v interface{}) *http.Response {
	var body []byte
	if sm, ok := v.(*manifest.SignedManifest); ok {
		body = sm.Raw
	} else {
		var err error
		body, err = json.MarshalIndent(v, "", "   ")
		if err != nil {
			t.Fatalf("unexpected error marshaling %v: %v", v, err)
		}
	}

	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("error creating request for %s: %v", msg, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("error doing put request while %s: %v", msg, err)
	}

	return resp
}

func startPushLayer(t *testing.T, ub *v2.URLBuilder, name string) string {
	layerUploadURL, err := ub.BuildBlobUploadURL(name)
	if err != nil {
		t.Fatalf("unexpected error building layer upload url: %v", err)
	}

	resp, err := http.Post(layerUploadURL, "", nil)
	if err != nil {
		t.Fatalf("unexpected error starting layer push: %v", err)
	}
	defer resp.Body.Close()

	checkResponse(t, fmt.Sprintf("pushing starting layer push %v", name), resp, http.StatusAccepted)
	checkHeaders(t, resp, http.Header{
		"Location":       []string{"*"},
		"Content-Length": []string{"0"},
	})

	return resp.Header.Get("Location")
}

// doPushLayer pushes the layer content returning the url on success returning
// the response. If you're only expecting a successful response, use pushLayer.
func doPushLayer(t *testing.T, ub *v2.URLBuilder, name string, dgst digest.Digest, uploadURLBase string, body io.Reader) (*http.Response, error) {
	u, err := url.Parse(uploadURLBase)
	if err != nil {
		t.Fatalf("unexpected error parsing pushLayer url: %v", err)
	}

	u.RawQuery = url.Values{
		"_state": u.Query()["_state"],

		"digest": []string{dgst.String()},
	}.Encode()

	uploadURL := u.String()

	// Just do a monolithic upload
	req, err := http.NewRequest("PUT", uploadURL, body)
	if err != nil {
		t.Fatalf("unexpected error creating new request: %v", err)
	}

	return http.DefaultClient.Do(req)
}

// pushLayer pushes the layer content returning the url on success.
func pushLayer(t *testing.T, ub *v2.URLBuilder, name string, dgst digest.Digest, uploadURLBase string, body io.Reader) string {
	resp, err := doPushLayer(t, ub, name, dgst, uploadURLBase, body)
	if err != nil {
		t.Fatalf("unexpected error doing push layer request: %v", err)
	}
	defer resp.Body.Close()

	checkResponse(t, "putting monolithic chunk", resp, http.StatusCreated)

	expectedLayerURL, err := ub.BuildBlobURL(name, dgst)
	if err != nil {
		t.Fatalf("error building expected layer url: %v", err)
	}

	checkHeaders(t, resp, http.Header{
		"Location":       []string{expectedLayerURL},
		"Content-Length": []string{"0"},
	})

	return resp.Header.Get("Location")
}

func checkResponse(t *testing.T, msg string, resp *http.Response, expectedStatus int) {
	if resp.StatusCode != expectedStatus {
		t.Logf("unexpected status %s: %v != %v", msg, resp.StatusCode, expectedStatus)
		maybeDumpResponse(t, resp)

		t.FailNow()
	}
}

// checkBodyHasErrorCodes ensures the body is an error body and has the
// expected error codes, returning the error structure, the json slice and a
// count of the errors by code.
func checkBodyHasErrorCodes(t *testing.T, msg string, resp *http.Response, errorCodes ...v2.ErrorCode) (v2.Errors, []byte, map[v2.ErrorCode]int) {
	p, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("unexpected error reading body %s: %v", msg, err)
	}

	var errs v2.Errors
	if err := json.Unmarshal(p, &errs); err != nil {
		t.Fatalf("unexpected error decoding error response: %v", err)
	}

	if len(errs.Errors) == 0 {
		t.Fatalf("expected errors in response")
	}

	// TODO(stevvooe): Shoot. The error setup is not working out. The content-
	// type headers are being set after writing the status code.
	// if resp.Header.Get("Content-Type") != "application/json; charset=utf-8" {
	// 	t.Fatalf("unexpected content type: %v != 'application/json'",
	// 		resp.Header.Get("Content-Type"))
	// }

	expected := map[v2.ErrorCode]struct{}{}
	counts := map[v2.ErrorCode]int{}

	// Initialize map with zeros for expected
	for _, code := range errorCodes {
		expected[code] = struct{}{}
		counts[code] = 0
	}

	for _, err := range errs.Errors {
		if _, ok := expected[err.Code]; !ok {
			t.Fatalf("unexpected error code %v encountered during %s: %s ", err.Code, msg, string(p))
		}
		counts[err.Code]++
	}

	// Ensure that counts of expected errors were all non-zero
	for code := range expected {
		if counts[code] == 0 {
			t.Fatalf("expected error code %v not encounterd during %s: %s", code, msg, string(p))
		}
	}

	return errs, p, counts
}

func maybeDumpResponse(t *testing.T, resp *http.Response) {
	if d, err := httputil.DumpResponse(resp, true); err != nil {
		t.Logf("error dumping response: %v", err)
	} else {
		t.Logf("response:\n%s", string(d))
	}
}

// matchHeaders checks that the response has at least the headers. If not, the
// test will fail. If a passed in header value is "*", any non-zero value will
// suffice as a match.
func checkHeaders(t *testing.T, resp *http.Response, headers http.Header) {
	for k, vs := range headers {
		if resp.Header.Get(k) == "" {
			t.Fatalf("response missing header %q", k)
		}

		for _, v := range vs {
			if v == "*" {
				// Just ensure there is some value.
				if len(resp.Header[k]) > 0 {
					continue
				}
			}

			for _, hv := range resp.Header[k] {
				if hv != v {
					t.Fatalf("header value not matched in response: %q != %q", hv, v)
				}
			}
		}
	}
}
