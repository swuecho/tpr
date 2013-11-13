package main

import (
	"bytes"
	"code.google.com/p/go.crypto/scrypt"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/JackC/form"
	"github.com/JackC/pgx"
	qv "github.com/JackC/quo_vadis"
	"github.com/kylelemons/go-gypsy/yaml"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var pool *pgx.ConnectionPool

var config struct {
	configPath    string
	listenAddress string
	listenPort    string
}

var registrationFormTemplate *form.FormTemplate

func initialize() {
	var err error
	var yf *yaml.File

	flag.StringVar(&config.listenAddress, "address", "127.0.0.1", "address to listen on")
	flag.StringVar(&config.listenPort, "port", "8080", "port to listen on")
	flag.StringVar(&config.configPath, "config", "config.yml", "path to config file")
	flag.Parse()

	givenCliArgs := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		givenCliArgs[f.Name] = true
	})

	if config.configPath, err = filepath.Abs(config.configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid config path: %v\n", err)
		os.Exit(1)
	}

	if yf, err = yaml.ReadFile(config.configPath); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	if !givenCliArgs["address"] {
		if address, err := yf.Get("address"); err == nil {
			config.listenAddress = address
		}
	}

	if !givenCliArgs["port"] {
		if port, err := yf.Get("port"); err == nil {
			config.listenPort = port
		}
	}

	var connectionParameters pgx.ConnectionParameters
	if connectionParameters, err = extractConnectionOptions(yf); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	connectionParameters.Logger = logger

	if err = migrate(connectionParameters); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	poolOptions := pgx.ConnectionPoolOptions{MaxConnections: 10, AfterConnect: afterConnect, Logger: logger}
	pool, err = pgx.NewConnectionPool(connectionParameters, poolOptions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to create database connection pool: %v\n", err)
		os.Exit(1)
	}

	registrationFormTemplate = form.NewFormTemplate()
	registrationFormTemplate.AddField(&form.StringTemplate{Name: "name", Required: true, MaxLength: 30})
	registrationFormTemplate.AddField(&form.StringTemplate{Name: "password", Required: true, MinLength: 8, MaxLength: 50})
	registrationFormTemplate.AddField(&form.StringTemplate{Name: "passwordConfirmation", Required: true, MaxLength: 50})
	registrationFormTemplate.CustomValidate = func(f *form.Form) {
		password := f.Fields["password"]
		confirmation := f.Fields["passwordConfirmation"]
		if password.Error == nil && confirmation.Error == nil && password.Parsed != confirmation.Parsed {
			confirmation.Error = errors.New("does not match password")
		}
	}
}

func extractConnectionOptions(config *yaml.File) (connectionOptions pgx.ConnectionParameters, err error) {
	connectionOptions.Host, _ = config.Get("database.host")
	connectionOptions.Socket, _ = config.Get("database.socket")
	if connectionOptions.Host == "" && connectionOptions.Socket == "" {
		err = errors.New("Config must contain database.host or database.socket but it does not")
		return
	}
	port, _ := config.GetInt("database.port")
	connectionOptions.Port = uint16(port)
	if connectionOptions.Database, err = config.Get("database.database"); err != nil {
		err = errors.New("Config must contain database.database but it does not")
		return
	}
	if connectionOptions.User, err = config.Get("database.user"); err != nil {
		err = errors.New("Config must contain database.user but it does not")
		return
	}
	connectionOptions.Password, _ = config.Get("database.password")
	return
}

// afterConnect creates the prepared statements that this application uses
func afterConnect(conn *pgx.Connection) (err error) {
	err = conn.Prepare("getUnreadItems", `
		select coalesce(json_agg(row_to_json(t)), '[]'::json)
		from (
			select
				items.id,
				feeds.id as feed_id,
				feeds.name,
				items.title,
				items.url,
				publication_time
			from feeds
				join items on feeds.id=items.feed_id
				join unread_items on items.id=unread_items.item_id
			where user_id=$1
			order by publication_time asc
		) t`)
	if err != nil {
		return
	}

	err = conn.Prepare("deleteSession", `delete from sessions where id=$1`)
	if err != nil {
		return
	}

	err = conn.Prepare("getFeedsForUser", `
		select json_agg(row_to_json(t))
		from (
			select feeds.name, feeds.url, feeds.last_fetch_time
			from feeds
			  join subscriptions on feeds.id=subscriptions.feed_id
			where user_id=$1
			order by name
		) t`)
	if err != nil {
		return
	}

	return
}

type ApiSecureHandlerFunc func(w http.ResponseWriter, req *http.Request, env *environment)

func (f ApiSecureHandlerFunc) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	env := CreateEnvironment(req)
	if env.CurrentAccount() == nil {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "Bad or missing sessionID")
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
		sessionID, err = hex.DecodeString(env.request.FormValue("sessionID"))
		if err != nil {
			logger.Warning(fmt.Sprintf(`Bad or missing to sessionID "%s": %v`, env.request.FormValue("sessionID"), err))
			return nil
		}
		if session, present = getSession(sessionID); !present {
			return nil
		}

		var name interface{}
		// TODO - this could be an error from no records found -- or the connection could be dead or we could have a syntax error...
		name, err = pool.SelectValue("select name from users where id=$1", session.userID)
		if err == nil {
			env.currentAccount = &currentAccount{id: session.userID, name: name.(string)}
		}
	}
	return env.currentAccount
}

func RegisterHandler(w http.ResponseWriter, req *http.Request) {
	var registration struct {
		Name                 string `json:"name"`
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

	if userID, err := CreateUser(registration.Name, registration.Password); err == nil {
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
		if strings.Contains(err.Error(), "users_name_unq") {
			w.WriteHeader(422)
			fmt.Fprintln(w, `"name" is already taken`)
			return
		} else {
			panic(err.Error())
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

func AuthenticateUser(name, password string) (userID int32, err error) {
	var passwordDigest []byte
	var passwordSalt []byte

	err = pool.SelectFunc("select id, password_digest, password_salt from users where name=$1", func(r *pgx.DataRowReader) (err error) {
		userID = r.ReadValue().(int32)
		passwordDigest = r.ReadValue().([]byte)
		passwordSalt = r.ReadValue().([]byte)
		return
	}, name)

	if err != nil {
		return
	}

	var digest []byte
	digest, _ = scrypt.Key([]byte(password), passwordSalt, 16384, 8, 1, 32)

	if !bytes.Equal(digest, passwordDigest) {
		err = fmt.Errorf("Bad user name or password")
	}
	return
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

	if userID, err := AuthenticateUser(credentials.Name, credentials.Password); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)

		var response struct {
			Name      string `json:"name"`
			SessionID string `json:"sessionID"`
		}

		response.Name = credentials.Name
		response.SessionID = hex.EncodeToString(createSession(userID))

		encoder := json.NewEncoder(w)
		encoder.Encode(response)
	} else {
		w.WriteHeader(422)
		fmt.Fprintln(w, `Bad user name or password`)
		return
	}
}

func DeleteSessionHandler(w http.ResponseWriter, req *http.Request) {
	cookie := &http.Cookie{Name: "sessionId", Value: "logged out", Expires: time.Unix(0, 0)}
	http.SetCookie(w, cookie)
	http.Redirect(w, req, "/login", http.StatusSeeOther)
}

func GetUnreadItemsHandler(w http.ResponseWriter, req *http.Request, env *environment) {
	w.Header().Set("Content-Type", "application/json")
	if err := pool.SelectValueTo(w, "getUnreadItems", env.CurrentAccount().id); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func createSessionCookie(sessionId []byte) *http.Cookie {
	return &http.Cookie{Name: "sessionId", Value: hex.EncodeToString(sessionId)}
}

func GetFeedsHandler(w http.ResponseWriter, req *http.Request, env *environment) {
	fmt.Println("foo")
	w.Header().Set("Content-Type", "application/json")
	if err := pool.SelectValueTo(w, "getFeedsForUser", env.CurrentAccount().id); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func NoDirListing(handler http.Handler) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func IndexHtmlHandler(w http.ResponseWriter, req *http.Request) {
	http.ServeFile(w, req, "public/index.html")
}

func main() {
	initialize()
	router := qv.NewRouter()

	router.Post("/register", http.HandlerFunc(RegisterHandler))
	router.Post("/sessions", http.HandlerFunc(CreateSessionHandler))
	router.Delete("/sessions/:id", http.HandlerFunc(DeleteSessionHandler))
	router.Post("/subscriptions", ApiSecureHandlerFunc(CreateSubscriptionHandler))
	router.Get("/feeds", ApiSecureHandlerFunc(GetFeedsHandler))
	router.Get("/items/unread", ApiSecureHandlerFunc(GetUnreadItemsHandler))
	http.Handle("/api/", http.StripPrefix("/api", router))

	http.Handle("/", http.HandlerFunc(IndexHtmlHandler))
	http.Handle("/css/", NoDirListing(http.FileServer(http.Dir("./public/"))))
	http.Handle("/js/", NoDirListing(http.FileServer(http.Dir("./public/"))))

	listenAt := fmt.Sprintf("%s:%s", config.listenAddress, config.listenPort)
	fmt.Printf("Starting to listen on: %s\n", listenAt)

	go KeepFeedsFresh()

	if err := http.ListenAndServe(listenAt, nil); err != nil {
		os.Stderr.WriteString("Could not start web server!\n")
		os.Exit(1)
	}
}
