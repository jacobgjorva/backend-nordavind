package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/store"
)

func (s *Server) currentUser(r *http.Request) (store.User, bool) {
	user, ok := r.Context().Value(userKey).(store.User)
	return user, ok
}

// handleListChats returnerer brukerens samtaler.
func (s *Server) handleListChats(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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

// handleGenerateChatTitle lager en kort tittel fra første utveksling og
// lagrer den på samtalen.
func (s *Server) handleGenerateChatTitle(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "ikke innlogget", http.StatusUnauthorized)
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

	title := s.generateTitle(r, req.Question, req.Answer)
	if title == "" {
		// Fallback: første del av spørsmålet
		title = req.Question
		if t := []rune(title); len(t) > 60 {
			title = string(t[:60])
		}
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
