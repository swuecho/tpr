package main

import (
	"bytes"
	"code.google.com/p/go.crypto/scrypt"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/JackC/cli"
	"github.com/JackC/pgx"
	qv "github.com/JackC/quo_vadis"
	"github.com/vaughan0/go-ini"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const version = "0.5.1"

var repo repository

var config struct {
	configPath    string
	listenAddress string
	listenPort    string
	staticURL     string
}

func extractConnectionOptions(file ini.File) (connectionOptions pgx.ConnectionParameters, err error) {
	connectionOptions.Host, _ = file.Get("database", "host")
	connectionOptions.Socket, _ = file.Get("database", "socket")
	if connectionOptions.Host == "" && connectionOptions.Socket == "" {
		err = errors.New("Config must contain database.host or database.socket but it does not")
		return
	}

	if p, ok := file.Get("database", "port"); ok {
		n, err := strconv.ParseUint(p, 10, 16)
		connectionOptions.Port = uint16(n)
		if err != nil {
			return connectionOptions, err
		}
	}

	var ok bool

	if connectionOptions.Database, ok = file.Get("database", "database"); !ok {
		err = errors.New("Config must contain database.database but it does not")
		return
	}
	if connectionOptions.User, ok = file.Get("database", "user"); !ok {
		err = errors.New("Config must contain database.user but it does not")
		return
	}
	connectionOptions.Password, _ = file.Get("database", "password")
	return
}

type ApiSecureHandlerFunc func(w http.ResponseWriter, req *http.Request, env *environment)

func (f ApiSecureHandlerFunc) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	env := CreateEnvironment(req)
	if env.CurrentAccount() == nil {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "Bad or missing X-Authentication header")
		return
	}
	f(w, req, env)
}

type currentAccount struct {
	id   int32
	name string
}

type environment struct {
	request        *http.Request
	currentAccount *currentAccount
}

func CreateEnvironment(req *http.Request) *environment {
	return &environment{request: req}
}

func (env *environment) CurrentAccount() *currentAccount {
	if env.currentAccount == nil {
		var session Session
		var err error
		var present bool

		var sessionID []byte
		sessionID, err = hex.DecodeString(env.request.Header.Get("X-Authentication"))
		if err != nil {
			logger.Warning("tpr", fmt.Sprintf(`Bad or missing to X-Authenticaton header "%s": %v`, env.request.Header.Get("X-Authentication"), err))
			return nil
		}
		if session, present = getSession(sessionID); !present {
			return nil
		}

		// TODO - this could be an error from no records found -- or the connection could be dead or we could have a syntax error...
		user, err := repo.GetUser(session.userID)
		if err == nil {
			env.currentAccount = &currentAccount{id: user.ID.MustGet(), name: user.Name.MustGet()}
		}
	}
	return env.currentAccount
}

func RegisterHandler(w http.ResponseWriter, req *http.Request) {
	var registration struct {
		Name                 string `json:"name"`
		Email                string `json:"email"`
		Password             string `json:"password"`
		PasswordConfirmation string `json:"passwordConfirmation"`
	}

	decoder := json.NewDecoder(req.Body)
	if err := decoder.Decode(&registration); err != nil {
		w.WriteHeader(422)
		fmt.Fprintf(w, "Error decoding request: %v", err)
		return
	}

	if registration.Name == "" {
		w.WriteHeader(422)
		fmt.Fprintln(w, `Request must include the attribute "name"`)
		return
	}

	if len(registration.Name) > 30 {
		w.WriteHeader(422)
		fmt.Fprintln(w, `"name" must be less than 30 characters`)
		return
	}

	if len(registration.Password) < 8 {
		w.WriteHeader(422)
		fmt.Fprintln(w, `"password" must be at least than 8 characters`)
		return
	}

	if registration.Password != registration.PasswordConfirmation {
		w.WriteHeader(422)
		fmt.Fprintln(w, `"passwordConfirmation" must equal "password"`)
		return
	}

	if userID, err := CreateUser(registration.Name, registration.Email, registration.Password); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)

		var response struct {
			Name      string `json:"name"`
			SessionID string `json:"sessionID"`
		}

		response.Name = registration.Name
		response.SessionID = hex.EncodeToString(createSession(userID))

		encoder := json.NewEncoder(w)
		encoder.Encode(response)
	} else {
		if err, ok := err.(DuplicationError); ok {
			w.WriteHeader(422)
			fmt.Fprintf(w, `"%s" is already taken`, err.Field)
			return
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}
}

func CreateSubscriptionHandler(w http.ResponseWriter, req *http.Request, env *environment) {
	var subscription struct {
		URL string `json:"url"`
	}

	decoder := json.NewDecoder(req.Body)
	if err := decoder.Decode(&subscription); err != nil {
		w.WriteHeader(422)
		fmt.Fprintf(w, "Error decoding request: %v", err)
		return
	}

	if subscription.URL == "" {
		w.WriteHeader(422)
		fmt.Fprintln(w, `Request must include the attribute "url"`)
		return
	}

	if err := Subscribe(env.CurrentAccount().id, subscription.URL); err != nil {
		w.WriteHeader(422)
		fmt.Fprintln(w, `Bad user name or password`)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func DeleteSubscriptionHandler(w http.ResponseWriter, req *http.Request, env *environment) {
	feedID, err := strconv.ParseInt(req.FormValue("id"), 10, 32)
	if err != nil {
		// If not an integer it clearly can't be found
		http.NotFound(w, req)
		return
	}

	if err := repo.DeleteSubscription(env.CurrentAccount().id, int32(feedID)); err != nil {
		w.WriteHeader(422)
		fmt.Fprintf(w, "Error deleting subscription: %v", err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func AuthenticateUser(name, password string) (*User, error) {
	user, err := repo.GetUserByName(name)
	if err != nil {
		return nil, err
	}

	var digest []byte
	digest, _ = scrypt.Key([]byte(password), user.PasswordSalt, 16384, 8, 1, 32)

	if !bytes.Equal(digest, user.PasswordDigest) {
		err = fmt.Errorf("Bad user name or password")
	}
	return user, err
}

func CreateSessionHandler(w http.ResponseWriter, req *http.Request) {
	var credentials struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}

	decoder := json.NewDecoder(req.Body)
	if err := decoder.Decode(&credentials); err != nil {
		w.WriteHeader(422)
		fmt.Fprintf(w, "Error decoding request: %v", err)
		return
	}

	if credentials.Name == "" {
		w.WriteHeader(422)
		fmt.Fprintln(w, `Request must include the attribute "name"`)
		return
	}

	if credentials.Password == "" {
		w.WriteHeader(422)
		fmt.Fprintln(w, `Request must include the attribute "password"`)
		return
	}

	if user, err := AuthenticateUser(credentials.Name, credentials.Password); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)

		var response struct {
			Name      string `json:"name"`
			SessionID string `json:"sessionID"`
		}

		response.Name = credentials.Name
		response.SessionID = hex.EncodeToString(createSession(user.ID.MustGet()))

		encoder := json.NewEncoder(w)
		encoder.Encode(response)
	} else {
		w.WriteHeader(422)
		fmt.Fprintln(w, `Bad user name or password`)
		return
	}
}

func DeleteSessionHandler(w http.ResponseWriter, req *http.Request) {
	sessionID, err := hex.DecodeString(req.FormValue("id"))
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	err = deleteSession(sessionID)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	cookie := &http.Cookie{Name: "sessionId", Value: "logged out", Expires: time.Unix(0, 0)}
	http.SetCookie(w, cookie)
}

func GetUnreadItemsHandler(w http.ResponseWriter, req *http.Request, env *environment) {
	w.Header().Set("Content-Type", "application/json")
	if err := repo.CopyUnreadItemsAsJSONByUserID(w, env.CurrentAccount().id); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func MarkItemReadHandler(w http.ResponseWriter, req *http.Request, env *environment) {
	itemID, err := strconv.ParseInt(req.FormValue("id"), 10, 32)
	if err != nil {
		// If not an integer it clearly can't be found
		http.NotFound(w, req)
		return
	}

	err = repo.MarkItemRead(env.CurrentAccount().id, int32(itemID))
	if err == notFound {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func MarkMultipleItemsReadHandler(w http.ResponseWriter, req *http.Request, env *environment) {
	var request struct {
		ItemIDs []int32 `json:"itemIDs"`
	}

	decoder := json.NewDecoder(req.Body)
	if err := decoder.Decode(&request); err != nil {
		w.WriteHeader(422)
		fmt.Fprintf(w, "Error decoding request: %v", err)
		return
	}

	for _, itemID := range request.ItemIDs {
		err := repo.MarkItemRead(env.CurrentAccount().id, itemID)
		if err != nil && err != notFound {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
	}
}

func createSessionCookie(sessionId []byte) *http.Cookie {
	return &http.Cookie{Name: "sessionId", Value: hex.EncodeToString(sessionId)}
}

func ImportFeedsHandler(w http.ResponseWriter, req *http.Request, env *environment) {
	file, _, err := req.FormFile("file")
	if err != nil {
		w.WriteHeader(422)
		fmt.Fprintln(w, `No uploaded file found`)
		return
	}
	defer file.Close()

	var doc OpmlDocument
	err = xml.NewDecoder(file).Decode(&doc)
	if err != nil {
		w.WriteHeader(422)
		fmt.Fprintln(w, "Error parsing OPML upload")
		return
	}

	type subscriptionResult struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Success bool   `json:"success"`
	}

	results := make([]subscriptionResult, 0, len(doc.Body.Outlines))
	resultsChan := make(chan subscriptionResult)

	for _, outline := range doc.Body.Outlines {
		go func(outline OpmlOutline) {
			r := subscriptionResult{Title: outline.Title, URL: outline.URL}
			err := Subscribe(env.CurrentAccount().id, outline.URL)
			r.Success = err == nil
			resultsChan <- r
		}(outline)
	}

	for _ = range doc.Body.Outlines {
		r := <-resultsChan
		results = append(results, r)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func GetFeedsHandler(w http.ResponseWriter, req *http.Request, env *environment) {
	w.Header().Set("Content-Type", "application/json")
	if err := repo.CopySubscriptionsForUserAsJSON(w, env.CurrentAccount().id); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func main() {
	app := cli.NewApp()
	app.Name = "tpr"
	app.Usage = "The Pithy Reader RSS Aggregator"
	app.Version = version
	app.Author = "Jack Christensen"
	app.Email = "jack@jackchristensen.com"

	app.Commands = []cli.Command{
		{
			Name:        "server",
			ShortName:   "s",
			Usage:       "run the server",
			Synopsis:    "[command options]",
			Description: "run the tpr server",
			Flags: []cli.Flag{
				cli.StringFlag{"address, a", "127.0.0.1", "address to listen on"},
				cli.StringFlag{"port, p", "8080", "port to listen on"},
				cli.StringFlag{"config, c", "config.conf", "path to config file"},
				cli.StringFlag{"static-url", "", "reverse proxy static asset requests to URL"},
			},
			Action: Serve,
		},
	}

	app.Run(os.Args)

}

func Serve(c *cli.Context) {
	var err error

	config.listenAddress = c.String("address")
	config.listenPort = c.String("port")
	config.configPath = c.String("config")
	config.staticURL = c.String("static-url")

	if config.configPath, err = filepath.Abs(config.configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid config path: %v\n", err)
		os.Exit(1)
	}

	file, err := ini.LoadFile(config.configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	var ok bool

	if !c.IsSet("address") {
		if config.listenAddress, ok = file.Get("server", "address"); !ok {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	}

	if !c.IsSet("port") {
		if config.listenPort, ok = file.Get("server", "port"); !ok {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	}

	var connectionParameters pgx.ConnectionParameters
	if connectionParameters, err = extractConnectionOptions(file); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	pgxLogger := &PackageLogger{logger: logger, pkg: "pgx"}
	connectionParameters.Logger = pgxLogger

	poolOptions := pgx.ConnectionPoolOptions{MaxConnections: 10, AfterConnect: afterConnect, Logger: pgxLogger}

	repo, err = NewPgxRepository(connectionParameters, poolOptions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to create pgx repository: %v\n", err)
		os.Exit(1)
	}

	router := qv.NewRouter()

	router.Post("/register", http.HandlerFunc(RegisterHandler))
	router.Post("/sessions", http.HandlerFunc(CreateSessionHandler))
	router.Delete("/sessions/:id", http.HandlerFunc(DeleteSessionHandler))
	router.Post("/subscriptions", ApiSecureHandlerFunc(CreateSubscriptionHandler))
	router.Delete("/subscriptions/:id", ApiSecureHandlerFunc(DeleteSubscriptionHandler))
	router.Get("/feeds", ApiSecureHandlerFunc(GetFeedsHandler))
	router.Post("/feeds/import", ApiSecureHandlerFunc(ImportFeedsHandler))
	router.Get("/items/unread", ApiSecureHandlerFunc(GetUnreadItemsHandler))
	router.Post("/items/unread/mark_multiple_read", ApiSecureHandlerFunc(MarkMultipleItemsReadHandler))
	router.Delete("/items/unread/:id", ApiSecureHandlerFunc(MarkItemReadHandler))
	http.Handle("/api/", http.StripPrefix("/api", router))

	if config.staticURL != "" {
		staticURL, err := url.Parse(config.staticURL)
		if err != nil {
			logger.Fatal("tpr", fmt.Sprintf("Bad static-url: %v", err))
			os.Exit(1)
		}
		http.Handle("/", httputil.NewSingleHostReverseProxy(staticURL))
	}

	listenAt := fmt.Sprintf("%s:%s", config.listenAddress, config.listenPort)
	fmt.Printf("Starting to listen on: %s\n", listenAt)

	go KeepFeedsFresh()

	if err := http.ListenAndServe(listenAt, nil); err != nil {
		os.Stderr.WriteString("Could not start web server!\n")
		os.Exit(1)
	}
}
