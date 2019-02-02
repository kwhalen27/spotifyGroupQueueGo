package main

import (
	"log"
	"os"
	"net/http"
	"encoding/gob"
	"text/template"
	"github.com/gorilla/sessions"
	"github.com/gorilla/context"
	"github.com/zmb3/spotify"
	"golang.org/x/oauth2"
	"math/rand"
	"encoding/base64"
	"database/sql"
	"spotifyGroupQueueGo/wsHub"
	"spotifyGroupQueueGo/userStore"
)

type pageInfo struct {
	User interface{}
	Code string
	IsActive bool
	IsOwner bool
	QueueSongs []spotify.PlaylistTrack
	PlaylistExists bool
	IsLoggedIn bool
	HasVetoed bool
	VetoCount int
	UserCount int
	ShareableLink string
}

var key = []byte(os.Getenv("SESSION_KEY"))

// Uppercase so it can be accessed by the api
var Store = sessions.NewCookieStore(key)

var redirectURI = os.Getenv("redirectURI")
var auth spotify.Authenticator

var Db *sql.DB
var connString = os.Getenv("DATABASE_URL")

var WsHub = wsHub.NewHub()
var UStore = userStore.NewStore()

// https://github.com/GoogleCloudPlatform/golang-samples/blob/master/getting-started/bookshelf/app/auth.go
func init() {
	// Used for heroku
	if redirectURI == "" {
		redirectURI = "http://localhost:8080/spotify-callback"
	}
	
	auth = spotify.NewAuthenticator(redirectURI, spotify.ScopeUserReadEmail, spotify.ScopePlaylistModifyPublic,
									spotify.ScopeUserReadCurrentlyPlaying)
	gob.Register(&oauth2.Token{})	

	var err error

	// Connect to the DB
	Db, err = sql.Open("postgres", connString)

	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	// For Heroku
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	
	http.HandleFunc("/", loginHandler);
	http.HandleFunc("/favicon.ico", faviconHandler)
	http.HandleFunc("/logout", logoutHandler)
	http.HandleFunc("/spotify-callback", spotifyCallbackHandler)

	http.HandleFunc("/profile", profileHandler)
	http.HandleFunc("/search", SearchHandler)
	http.HandleFunc("/add", AddToQueueHandler)
	http.HandleFunc("/join", JoinRoomHandler)

	http.HandleFunc("/room/open", func(w http.ResponseWriter, r *http.Request) {
		OpenRoomHandler(WsHub, w, r)
	})
	http.HandleFunc("/room/close", CloseRoomHandler)
	http.HandleFunc("/room/veto", VetoHandler)
	http.HandleFunc("/room/", roomHandler)

	http.HandleFunc("/playlist/create", CreatePlaylistHandler)
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		wsHub.WsHandler(WsHub, Store, UStore,  w, r)
	})

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))
	http.ListenAndServe(":" + port, context.ClearHandler(http.DefaultServeMux))
}

func faviconHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "favicon.ico")
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	session, err := Store.Get(r, "groupQueue")

	if err != nil {
		log.Println(err)
	}

	tok := session.Values["token"]

	// There is a user logged in already
	if tok != nil {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)	
	} else {
		// CSRF Protection with state
		b := make([]byte, 20)
		rand.Read(b)
		state := base64.StdEncoding.EncodeToString(b)

		session.Values["state"] = state
		session.Save(r, w)

		url := auth.AuthURL(state)

		tmpl := template.Must(template.ParseFiles("templates/index.html"))
		tmpl.Execute(w, url)
	}
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	session, err := Store.Get(r, "groupQueue")

	if err != nil {
		log.Println(err)
	}

	tok, ok := session.Values["token"].(*oauth2.Token)

	if !ok {
		log.Println("Session value is not of type *oauth2.Token")
	}

	client := auth.NewClient(tok)
	user, err := client.CurrentUser()

	if err != nil {
		log.Println(err)
	}

	// Remove the room from the map
	DeleteRoom(Db, string(user.ID))	

	// Invalidate session
	session.Options.MaxAge = -1
	session.Save(r, w)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func spotifyCallbackHandler(w http.ResponseWriter, r *http.Request) {
	session, err := Store.Get(r, "groupQueue")

	if err != nil {
		log.Println(err)
	}

	state, ok := session.Values["state"].(string)

	if !ok {
		log.Println("Session value is not of type string")
	}

	tok, err := auth.Token(state, r)
	if err != nil {
		http.Error(w, "Couldn't get token", http.StatusForbidden)
		log.Fatal(err)
	}
	
	// CSRF Protection with state
	st := r.FormValue("state")
	if st != state {
		http.NotFound(w, r)
		log.Fatal("State mismatch.")
	}

	session.Values["token"] = tok
	session.Save(r, w)

	http.Redirect(w, r, "/profile", http.StatusSeeOther)
}

func profileHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("templates/profile.html"))

	session, err := Store.Get(r, "groupQueue")
	
	if err != nil {
		log.Println(err)
	}

	tok, ok := session.Values["token"].(*oauth2.Token)

	if !ok {
		log.Println("Session value is not of type *oauth2.Token")
	}

	// Generate an id for the session if one does not exist
	id, ok := session.Values["id"].(string)
	
	if !ok {	
		// Need a UUID for each user
		uuid := generateUUID()

		session.Values["id"] = uuid
		id = uuid

		session.Save(r, w)
	}

	isLoggedIn := false

	if tok != nil {
		isLoggedIn = true
	} else {
		// Need to login to see the profile page
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	client := auth.NewClient(tok)
	user, err := client.CurrentUser()

	if err != nil {
		log.Println(err)
	}

	roomCode := GetRoomCode(Db, string(user.ID))	
	active := DoesRoomExist(Db, roomCode)
	
	queueSongs, playlistExists := getQueueSongs(&client)

	hasVetoed := UStore.UserHasVoted(id, roomCode)
	shareable := r.Host + "/room/" + roomCode

	pInfo := pageInfo {
		User: user, 
		Code: roomCode, 
		IsActive: active, 
		IsOwner: true, 
		QueueSongs: queueSongs.Tracks,
		PlaylistExists: playlistExists, 
		IsLoggedIn: isLoggedIn, 
		HasVetoed: hasVetoed,
		VetoCount: UStore.GetVoteCount(roomCode), 
		UserCount: UStore.GetTotalUserCount(roomCode),
		ShareableLink: shareable,
	}

	if err = tmpl.Execute(w, pInfo); err != nil {
		log.Println(err)
	}
}

func roomHandler(w http.ResponseWriter, r *http.Request) {
	session, err := Store.Get(r, "groupQueue")

	if err != nil {
		log.Println(err)
	}

	tok, _ := session.Values["token"].(*oauth2.Token)

	// Generate an id for the session if one does not exist
	id, ok := session.Values["id"].(string)
	
	if !ok {	
		// Need a UUID for each user
		uuid := generateUUID()

		// Need to cut off at 7 chars (base64 can be longer)
		session.Values["id"] = uuid
		id = uuid

		session.Save(r, w)
	}

	isLoggedIn := false

	if tok != nil {
		isLoggedIn = true
	}

	roomCode := r.URL.Path[len("/room/"):]
	
	// See if the room code exists
	found := DoesRoomExist(Db, roomCode)

	if !found {	
		log.Printf("Room code %s not found.", roomCode)
		http.Redirect(w, r, "/static/room-not-found.html", http.StatusSeeOther)
		return
	}

	// Get the token
	tok = GetTokenFromCode(Db, roomCode)
	
	// Need a client to get the songs in the playlist
	client := auth.NewClient(tok)

	groupPlaylistId := GetPlaylistIdByName(&client, "GroupQueue")
	queueSongs, err := client.GetPlaylistTracks(groupPlaylistId)

	if err != nil {
		log.Println(err)
	}

	hasVetoed := UStore.UserHasVoted(id, roomCode)
	shareable := r.Host + "/room/" + roomCode

	pInfo := pageInfo {
		User: struct{ID string} {string(roomCode)}, 
		Code: string(roomCode),
		IsActive: true, 
		IsOwner: false, 
		QueueSongs: queueSongs.Tracks,
		PlaylistExists: false, 
		IsLoggedIn: isLoggedIn, 
		HasVetoed: hasVetoed,
		VetoCount: UStore.GetVoteCount(roomCode), 
		UserCount: UStore.GetTotalUserCount(roomCode),
		ShareableLink: shareable,
	}

	tmpl := template.Must(template.ParseFiles("templates/profile.html"))
	tmpl.Execute(w, pInfo) 
}
