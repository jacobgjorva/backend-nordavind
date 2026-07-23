package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

// titleFromText lager en kort samtaletittel uten LLM: første setning eller de
// første ordene av brukerens melding, trimmet til 60 tegn. Deterministisk =
// null tokens.
func titleFromText(question string) string {
	t := strings.TrimSpace(question)
	// Kutt ved første setningsslutt hvis den kommer tidlig.
	if i := strings.IndexAny(t, ".!?\n"); i > 0 && i < 60 {
		t = t[:i]
	}
	t = strings.TrimSpace(t)
	// Ellers: begrens til de første ~8 ordene.
	if fields := strings.Fields(t); len(fields) > 8 {
		t = strings.Join(fields[:8], " ")
	}
	if r := []rune(t); len(r) > 60 {
		t = strings.TrimSpace(string(r[:60]))
	}
	return t
}

// handleListChats returnerer brukerens samtaler.
func (s *Server) handleListChats(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	chats, err := s.store.ListChats(user.ID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if chats == nil {
		chats = []store.Chat{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"chats": chats})
}

// handleCreateChat oppretter en ny samtale.
func (s *Server) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Ny samtale"
	}
	if r := []rune(title); len(r) > 60 {
		title = string(r[:60])
	}

	chat, err := s.store.CreateChat(user.TenantID, user.ID, title)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chat)
}

// handleGetChat returnerer meldingene i en samtale.
func (s *Server) handleGetChat(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	messages, err := s.store.ChatMessages(r.PathValue("id"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if messages == nil {
		messages = []store.ChatMessage{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"messages": messages})
}

// handleAppendChatMessage legger til en melding i samtalen.
func (s *Server) handleAppendChatMessage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var m store.ChatMessage
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil ||
		(m.Role != "user" && m.Role != "assistant") || m.Content == "" {
		http.Error(w, "ugyldig melding", http.StatusBadRequest)
		return
	}
	err := s.store.AppendMessage(r.PathValue("id"), user.ID, m)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteChat sletter en samtale.
func (s *Server) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteChat(r.PathValue("id"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRenameChat setter en manuell tittel på samtalen.
func (s *Server) handleRenameChat(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		http.Error(w, "tom tittel", http.StatusBadRequest)
		return
	}
	if t := []rune(title); len(t) > 60 {
		title = string(t[:60])
	}
	err := s.store.UpdateChatTitle(r.PathValue("id"), user.ID, title)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"title": title})
}

// handleSetChatFolder flytter en chat inn i (eller ut av) en mappe.
func (s *Server) handleSetChatFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		FolderID string `json:"folder_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	err := s.store.SetChatFolder(r.PathValue("id"), user.ID, strings.TrimSpace(req.FolderID))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListFolders returnerer brukerens mapper.
func (s *Server) handleListFolders(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	folders, err := s.store.ListFolders(user.ID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if folders == nil {
		folders = []store.Folder{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"folders": folders})
}

// handleCreateFolder oppretter en mappe.
func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Ny mappe"
	}
	if t := []rune(name); len(t) > 60 {
		name = string(t[:60])
	}
	f, err := s.store.CreateFolder(user.TenantID, user.ID, name)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(f)
}

// handleRenameFolder endrer mappenavn.
func (s *Server) handleRenameFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "tomt navn", http.StatusBadRequest)
		return
	}
	if t := []rune(name); len(t) > 60 {
		name = string(t[:60])
	}
	err := s.store.RenameFolder(r.PathValue("id"), user.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteFolder sletter en mappe (chattene løsnes).
func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteFolder(r.PathValue("id"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGenerateChatTitle lager en kort tittel fra første utveksling og
// lagrer den på samtalen.
func (s *Server) handleGenerateChatTitle(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}

	title := titleFromText(req.Question)

	err := s.store.UpdateChatTitle(r.PathValue("id"), user.ID, title)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"title": title})
}
