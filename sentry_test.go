package logrus_sentry

import (
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/musqdp/raven-go"
	pkgerrors "github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

const (
	message     = "error message"
	server_name = "testserver.internal"
	logger_name = "test.logger"
)

func getTestLogger() *logrus.Logger {
	l := logrus.New()
	l.Out = ioutil.Discard
	return l
}

// raven.Packet does not have a json directive for deserializing stacktrace
// so need to explicitly construct one for purpose of test
type resultPacket struct {
	raven.Packet
	Stacktrace raven.Stacktrace `json:"stacktrace"`
	Exception  raven.Exception  `json:"exception"`
}

func WithTestDSN(t *testing.T, tf func(string, <-chan *resultPacket)) {
	pch := make(chan *resultPacket, 1)
	s := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		defer req.Body.Close()
		contentType := req.Header.Get("Content-Type")
		var bodyReader io.Reader = req.Body
		// underlying client will compress and encode payload above certain size
		if contentType == "application/octet-stream" {
			bodyReader = base64.NewDecoder(base64.StdEncoding, bodyReader)
			bodyReader, _ = zlib.NewReader(bodyReader)
		}

		d := json.NewDecoder(bodyReader)
		p := &resultPacket{}
		err := d.Decode(p)
		if err != nil {
			t.Fatal(err.Error())
		}

		pch <- p
	}))
	defer s.Close()

	fragments := strings.SplitN(s.URL, "://", 2)
	dsn := fmt.Sprintf(
		"%s://public:secret@%s/sentry/project-id",
		fragments[0],
		fragments[1],
	)
	tf(dsn, pch)
}

func TestSpecialFields(t *testing.T) {
	WithTestDSN(t, func(dsn string, pch <-chan *resultPacket) {
		logger := getTestLogger()

		hook, err := NewSentryHook(dsn, []logrus.Level{
			logrus.ErrorLevel,
		})

		if err != nil {
			t.Fatal(err.Error())
		}
		logger.Hooks.Add(hook)

		req, _ := http.NewRequest("GET", "url", nil)
		logger.WithFields(logrus.Fields{
			"logger":       logger_name,
			"server_name":  server_name,
			"http_request": req,
		}).Error(message)

		packet := <-pch
		if packet.Logger != logger_name {
			t.Errorf("logger should have been %s, was %s", logger_name, packet.Logger)
		}

		if packet.ServerName != server_name {
			t.Errorf("server_name should have been %s, was %s", server_name, packet.ServerName)
		}
	})
}

func TestSentryHandler(t *testing.T) {
	WithTestDSN(t, func(dsn string, pch <-chan *resultPacket) {
		logger := getTestLogger()
		hook, err := NewSentryHook(dsn, []logrus.Level{
			logrus.ErrorLevel,
		})
		if err != nil {
			t.Fatal(err.Error())
		}
		logger.Hooks.Add(hook)

		logger.Error(message)
		packet := <-pch
		if packet.Message != message {
			t.Errorf("message should have been %s, was %s", message, packet.Message)
		}
	})
}

func TestSentryWithClient(t *testing.T) {
	WithTestDSN(t, func(dsn string, pch <-chan *resultPacket) {
		logger := getTestLogger()

		client, _ := raven.New(dsn)

		hook, err := NewWithClientSentryHook(client, []logrus.Level{
			logrus.ErrorLevel,
		})
		if err != nil {
			t.Fatal(err.Error())
		}
		logger.Hooks.Add(hook)

		logger.Error(message)
		packet := <-pch
		if packet.Message != message {
			t.Errorf("message should have been %s, was %s", message, packet.Message)
		}
	})
}

func TestSentryWithClientAndError(t *testing.T) {
	WithTestDSN(t, func(dsn string, pch <-chan *resultPacket) {
		logger := getTestLogger()

		client, _ := raven.New(dsn)

		hook, err := NewWithClientSentryHook(client, []logrus.Level{
			logrus.ErrorLevel,
		})
		if err != nil {
			t.Fatal(err.Error())
		}
		logger.Hooks.Add(hook)

		errorMsg := "error message"
		logger.WithError(errors.New(errorMsg)).Error(message)
		packet := <-pch
		if packet.Message != message {
			t.Errorf("message should have been %s, was %s", message, packet.Message)
		}
		if packet.Culprit != errorMsg {
			t.Errorf("culprit should have been %s, was %s", errorMsg, packet.Culprit)
		}
	})
}

func TestSentryTags(t *testing.T) {
	WithTestDSN(t, func(dsn string, pch <-chan *resultPacket) {
		logger := getTestLogger()
		tags := map[string]string{
			"site": "test",
		}
		levels := []logrus.Level{
			logrus.ErrorLevel,
		}

		hook, err := NewWithTagsSentryHook(dsn, tags, levels)
		if err != nil {
			t.Fatal(err.Error())
		}

		logger.Hooks.Add(hook)

		logger.Error(message)
		packet := <-pch
		expected := raven.Tags{
			raven.Tag{
				Key:   "site",
				Value: "test",
			},
		}
		if !reflect.DeepEqual(packet.Tags, expected) {
			t.Errorf("tags should have been %+v, was %+v", expected, packet.Tags)
		}
	})
}

func TestSentryFingerprint(t *testing.T) {
	WithTestDSN(t, func(dsn string, pch <-chan *resultPacket) {
		logger := getTestLogger()
		levels := []logrus.Level{
			logrus.ErrorLevel,
		}
		fingerprint := []string{"fingerprint"}

		hook, err := NewSentryHook(dsn, levels)
		if err != nil {
			t.Fatal(err.Error())
		}

		logger.Hooks.Add(hook)

		logger.WithFields(logrus.Fields{
			"fingerprint": fingerprint,
		}).Error(message)
		packet := <-pch
		if !reflect.DeepEqual(packet.Fingerprint, fingerprint) {
			t.Errorf("fingerprint should have been %v, was %v", fingerprint, packet.Fingerprint)
		}
	})
}

func TestAddIgnore(t *testing.T) {
	hook := SentryHook{
		ignoreFields: make(map[string]struct{}),
	}

	list := []string{"foo", "bar", "baz"}
	for i, key := range list {
		if len(hook.ignoreFields) != i {
			t.Errorf("hook.ignoreFields has %d length, but %d", i, len(hook.ignoreFields))
			continue
		}

		hook.AddIgnore(key)
		if len(hook.ignoreFields) != i+1 {
			t.Errorf("hook.ignoreFields should be added")
			continue
		}
		for j := 0; j <= i; j++ {
			k := list[j]
			if _, ok := hook.ignoreFields[k]; !ok {
				t.Errorf("%s should be added into hook.ignoreFields", k)
				continue
			}
		}
	}
}

func TestAddExtraFilter(t *testing.T) {
	hook := SentryHook{
		extraFilters: make(map[string]func(interface{}) interface{}),
	}

	list := []string{"foo", "bar", "baz"}
	for i, key := range list {
		if len(hook.extraFilters) != i {
			t.Errorf("hook.extraFilters has %d length, but %d", i, len(hook.extraFilters))
			continue
		}

		hook.AddExtraFilter(key, nil)
		if len(hook.extraFilters) != i+1 {
			t.Errorf("hook.extraFilters should be added")
			continue
		}
		for j := 0; j <= i; j++ {
			k := list[j]
			if _, ok := hook.extraFilters[k]; !ok {
				t.Errorf("%s should be added into hook.extraFilters", k)
				continue
			}
		}
	}
}

func TestFormatExtraData(t *testing.T) {
	hook := SentryHook{
		ignoreFields: make(map[string]struct{}),
		extraFilters: make(map[string]func(interface{}) interface{}),
	}
	hook.AddIgnore("ignore1")
	hook.AddIgnore("ignore2")
	hook.AddIgnore("ignore3")
	hook.AddExtraFilter("filter1", func(v interface{}) interface{} {
		return "filter1 value"
	})

	tests := []struct {
		isExist  bool
		key      string
		value    interface{}
		expected interface{}
	}{
		{true, "integer", 13, 13},
		{true, "string", "foo", "foo"},
		{true, "bool", true, true},
		{true, "time.Time", time.Time{}, "0001-01-01 00:00:00 +0000 UTC"},
		{true, "myStringer", myStringer{}, "myStringer!"},
		{true, "myStringer_ptr", &myStringer{}, "myStringer!"},
		{true, "notStringer", notStringer{}, notStringer{}},
		{true, "notStringer_ptr", &notStringer{}, &notStringer{}},
		{false, "ignore1", 13, false},
		{false, "ignore2", "foo", false},
		{false, "ignore3", time.Time{}, false},
		{true, "filter1", "filter1", "filter1 value"},
		{true, "filter1", time.Time{}, "filter1 value"},
	}

	for _, tt := range tests {
		target := fmt.Sprintf("%+v", tt)

		fields := logrus.Fields{
			"time_stamp":    time.Now(), // implements JSON marshaler
			"time_duration": time.Hour,  // implements .String()
			"err":           errors.New("this is a test error"),
			"order":         13,
			tt.key:          tt.value,
		}
		df := newDataField(fields)
		result := hook.formatExtraData(df)

		value, ok := result[tt.key]
		if !tt.isExist {
			if ok {
				t.Errorf("%s should not be exist. data=%s", tt.key, target)
			}
			continue
		}

		if fmt.Sprint(tt.expected) != fmt.Sprint(value) {
			t.Errorf("%s should be %v, but %v. data=%s", tt.key, tt.expected, value, target)
		}
	}
}

func TestFormatData(t *testing.T) {
	// assertion types
	var (
		assertTypeInt    int
		assertTypeString string
		assertTypeTime   time.Time
	)

	tests := []struct {
		name         string
		value        interface{}
		expectedType interface{}
	}{
		{"int", 13, assertTypeInt},
		{"string", "foo", assertTypeString},
		{"error", errors.New("this is a test error"), assertTypeString},
		{"time_stamp", time.Now(), assertTypeTime},        // implements JSON marshaler
		{"time_duration", time.Hour, assertTypeString},    // implements .String()
		{"stringer", myStringer{}, assertTypeString},      // implements .String()
		{"stringer_ptr", &myStringer{}, assertTypeString}, // implements .String()
		{"not_stringer", notStringer{}, notStringer{}},
		{"not_stringer_ptr", &notStringer{}, &notStringer{}},
	}

	for _, tt := range tests {
		target := fmt.Sprintf("%+v", tt)

		result := formatData(tt.value)

		resultType := reflect.TypeOf(result).String()
		expectedType := reflect.TypeOf(tt.expectedType).String()
		if resultType != expectedType {
			t.Errorf("invalid type: type should be %s, but %s. data=%s", resultType, expectedType, target)
		}
	}
}

type myStringer struct{}

func (myStringer) String() string { return "myStringer!" }

type notStringer struct{}

func (notStringer) String() {}

type myStacktracerError struct{}

func (myStacktracerError) Error() string { return "myStacktracerError!" }

const expectedStackFrameFilename = "errorFile.go"

func (myStacktracerError) GetStacktrace() *raven.Stacktrace {
	return &raven.Stacktrace{
		Frames: []*raven.StacktraceFrame{
			{Filename: expectedStackFrameFilename},
		},
	}
}

func TestConvertStackTrace(t *testing.T) {
	hook := SentryHook{}
	expected := raven.NewStacktrace(0, 0, nil)
	st := pkgerrors.New("-").(pkgErrorStackTracer).StackTrace()
	ravenSt := hook.convertStackTrace(st)

	// Obscure the line numbes, so DeepEqual doesn't fail erroneously
	for _, frame := range append(expected.Frames, ravenSt.Frames...) {
		frame.Lineno = 999
	}
	if !reflect.DeepEqual(ravenSt, expected) {
		t.Error("stack traces differ")
	}
}

func TestErrorHandler(t *testing.T) {
	a := assert.New(t)

	s, dsn := httptestNewServer(func(rw http.ResponseWriter, req *http.Request) {
		defer req.Body.Close()
		rw.WriteHeader(400)
	})
	defer s.Close()

	hook, err := NewSentryHook(dsn, []logrus.Level{
		logrus.ErrorLevel,
	})
	a.NoError(err, "NewSentryHook should be no error")

	logger := getTestLogger()
	logger.Hooks.Add(hook)

	hook.AddErrorHandler(func(e *logrus.Entry, err error) {
		a.Error(err, "ErrorHandler should capture error")
		a.Contains(err.Error(), "raven: got http status 400")
	})

	err = hook.Fire(&logrus.Entry{})
	a.Error(err, "hook.Fire should have error")
}

// create http test server
func httptestNewServer(handler func(http.ResponseWriter, *http.Request)) (server *httptest.Server, dsn string) {
	server = httptest.NewServer(http.HandlerFunc(handler))

	fragments := strings.SplitN(server.URL, "://", 2)
	dsn = fmt.Sprintf(
		"%s://public:secret@%s/sentry/project-id",
		fragments[0],
		fragments[1],
	)
	return server, dsn
}
