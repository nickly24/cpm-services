package tests

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
)

type cacheMongoCommand struct {
	name string
	raw  bson.Raw
}

func cacheMongoStore(t *testing.T, responses ...bson.D) (*Store, *[]cacheMongoCommand) {
	t.Helper()
	commands := []cacheMongoCommand{}
	opts := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, event *event.CommandStartedEvent) {
		if event.CommandName != "endSessions" {
			commands = append(commands, cacheMongoCommand{event.CommandName, append(bson.Raw{}, event.Command...)})
		}
	}})
	opts.Deployment = drivertest.NewMockDeployment(responses...)
	client, e := mongo.Connect(opts)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	return &Store{Collection: client.Database("ratings_unit").Collection(CollectionName)}, &commands
}

func cacheDocument() Document {
	return Document{ID: bson.NewObjectID(), OrganizationID: 7, OwnerDivisionID: 11, DirectionID: 13, Revision: 1, Title: "Исходный тест",
		ContentVersionID: bson.NewObjectID(), HistoryVersionIDs: []bson.ObjectID{bson.NewObjectID()}, UsedOptionIDs: map[string][]string{"1": {"a", "b"}},
		Placements: []Placement{{DivisionID: 11, Settings: Settings{Published: true}}},
		Questions:  []Question{{QuestionID: 1, Type: "single", Text: "Вопрос", Points: 2, Answers: []AnswerOption{{ID: "a", Text: "Верно", IsCorrect: true}, {ID: "b", Text: "Неверно"}}}}}
}

func TestCacheBoundedCopiesTTLAndGeneration(t *testing.T) {
	c := newDataCache()
	c.maxEntries, c.maxBytes = 2, 6
	now := time.Unix(100, 0)
	c.now = func() time.Time { return now }
	input := []byte("abc")
	if !c.put("a", input, time.Minute, c.epoch()) || !c.put("b", []byte("def"), time.Minute, c.epoch()) {
		t.Fatal("cache refused bounded entries")
	}
	input[0] = 'x'
	raw, _ := c.get("a")
	if string(raw) != "abc" {
		t.Fatal("cache retained caller-owned bytes")
	}
	raw[0] = 'x'
	if !c.put("c", []byte("ghi"), time.Minute, c.epoch()) {
		t.Fatal("cache refused replacement")
	}
	if _, hit := c.get("b"); hit {
		t.Fatal("least recently used entry survived capacity eviction")
	}
	if raw, _ := c.get("a"); string(raw) != "abc" {
		t.Fatal("returned bytes can mutate the cache")
	}
	now = now.Add(time.Minute)
	if _, hit := c.get("a"); hit {
		t.Fatal("expired entry returned at TTL boundary")
	}
	oldEpoch := c.epoch()
	c.invalidate(7, bson.NewObjectID())
	if c.put("late-mongo-read", []byte("abc"), time.Minute, oldEpoch) {
		t.Fatal("pre-CAS read repopulated cache after invalidation")
	}
	if c.put("oversized", make([]byte, cacheEntryLimit+1), time.Minute, c.epoch()) {
		t.Fatal("oversized entry entered cache")
	}
	if c.bytes > c.maxBytes || c.lru.Len() > c.maxEntries {
		t.Fatal("cache exceeded its bounds")
	}
}

func TestCacheRaceReadWriteInvalidate(t *testing.T) {
	c := newDataCache()
	id := bson.NewObjectID()
	var wg sync.WaitGroup
	for worker := 0; worker < 12; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				key := documentCacheKey(7, id, int64(i%4+1))
				c.put(key, []byte("payload"), time.Minute, c.epoch())
				if raw, hit := c.get(key); hit {
					raw[0] = 'x'
				}
				if i%3 == 0 {
					c.invalidate(7, id)
				}
			}
		}()
	}
	wg.Wait()
	c.invalidate(7, id)
	if len(c.entries) != 0 || c.bytes != 0 {
		t.Fatal("concurrent invalidation left document entries")
	}
}

func TestDocumentCacheUsesLiveRevisionAndIndependentValues(t *testing.T) {
	d := cacheDocument()
	next := d
	next.Revision, next.Title = 2, "Новая версия"
	next.ContentVersionID = bson.NewObjectID()
	s, commands := cacheMongoStore(t,
		ratingMongoBatch(CollectionName, stampsForDocuments([]Document{d})[0]), ratingMongoBatch(CollectionName, d),
		ratingMongoBatch(CollectionName, stampsForDocuments([]Document{d})[0]),
		ratingMongoBatch(CollectionName, stampsForDocuments([]Document{next})[0]), ratingMongoBatch(CollectionName, next),
		ratingMongoBatch(CollectionName, stampsForDocuments([]Document{next})[0]),
	)
	first, e := s.Get(context.Background(), d.OrganizationID, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	first.Title = "Мутация клиента"
	first.Placements[0].Settings.Published = false
	first.Questions[0].Answers[0].Text = "Мутация ответа"
	first.UsedOptionIDs["1"][0] = "mutation"
	second, e := s.Get(context.Background(), d.OrganizationID, d.ID)
	if e != nil || second.Title != d.Title || !second.Placements[0].Settings.Published || second.Questions[0].Answers[0].Text != "Верно" || second.UsedOptionIDs["1"][0] != "a" {
		t.Fatal("mutable slices/maps leaked between reads", second, e)
	}
	for i := 0; i < 2; i++ {
		current, err := s.Get(context.Background(), d.OrganizationID, d.ID)
		if err != nil || current.Revision != 2 || current.ContentVersionID != next.ContentVersionID || current.Title != next.Title {
			t.Fatal("cross-replica revision change returned stale document", current, err)
		}
	}
	if len(*commands) != 6 {
		t.Fatal("cache hit did not reduce full document reads", len(*commands))
	}
	projected := 0
	for _, command := range *commands {
		filter := command.raw.Lookup("filter").Document()
		if filter.Lookup("organization_id").Int64() != d.OrganizationID || filter.Lookup("_id").ObjectID() != d.ID {
			t.Fatal("document cache query lost its scope", command.raw)
		}
		if projection := command.raw.Lookup("projection"); projection.Type != 0 {
			projected++
			if projection.Document().Lookup("questions").Type != 0 || projection.Document().Lookup("revision").Type == 0 {
				t.Fatal("live revision lookup loads body or omits revision", command.raw)
			}
		}
	}
	if projected != 4 {
		t.Fatal("document hits skipped a live revision read", projected)
	}
}

func TestDocumentCacheFailsClosedOnMongoOutageOrDeletion(t *testing.T) {
	d := cacheDocument()
	s, _ := cacheMongoStore(t, ratingMongoBatch(CollectionName, d), ratingMongoBatch(CollectionName, d),
		bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 13}, {Key: "errmsg", Value: "unavailable"}}, ratingMongoBatch(CollectionName))
	if _, e := s.Get(context.Background(), d.OrganizationID, d.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Get(context.Background(), d.OrganizationID, d.ID); e == nil {
		t.Fatal("warm cache masked a Mongo error")
	}
	if _, e := s.Get(context.Background(), d.OrganizationID, d.ID); !errors.Is(e, missing) {
		t.Fatal("warm cache masked deletion", e)
	}
}

func TestCatalogCacheTracksLiveMembershipRevisionAndScope(t *testing.T) {
	d := cacheDocument()
	next := cacheDocument()
	next.Title = "Новый первый тест"
	s, commands := cacheMongoStore(t,
		ratingMongoBatch(CollectionName, d), ratingMongoBatch(CollectionName, d), ratingMongoBatch(CollectionName, d),
		ratingMongoBatch(CollectionName, next), ratingMongoBatch(CollectionName, next), ratingMongoBatch(CollectionName), ratingMongoBatch(CollectionName),
	)
	filter := bson.M{"organization_id": d.OrganizationID, "placements.division_id": int64(11)}
	first, e := s.List(context.Background(), filter, 0, 20)
	if e != nil || len(first) != 1 {
		t.Fatal(first, e)
	}
	first[0].Questions[0].Answers[0].Text = "client mutation"
	second, e := s.List(context.Background(), filter, 0, 20)
	if e != nil || len(second) != 1 || second[0].Questions[0].Answers[0].Text != "Верно" {
		t.Fatal("catalog cache returned shared values", second, e)
	}
	third, e := s.List(context.Background(), filter, 0, 20)
	if e != nil || len(third) != 1 || third[0].ID != next.ID {
		t.Fatal("catalog page membership change was hidden", third, e)
	}
	empty, e := s.List(context.Background(), filter, 0, 20)
	if e != nil || len(empty) != 0 {
		t.Fatal("catalog removal was hidden", empty, e)
	}
	if len(*commands) != 7 {
		t.Fatal("unexpected catalog payload reads", len(*commands))
	}
	key := catalogCacheKey(7, filter, 0, 20)
	for _, other := range []string{
		catalogCacheKey(8, bson.M{"organization_id": int64(8), "placements.division_id": int64(11)}, 0, 20),
		catalogCacheKey(7, bson.M{"organization_id": int64(7), "placements.division_id": int64(12)}, 0, 20),
		catalogCacheKey(7, filter, 20, 20), catalogCacheKey(7, filter, 0, 10),
		catalogCacheKey(7, bson.M{"organization_id": int64(7), "placements.division_id": int64(11), "direction_id": int64(13)}, 0, 20),
	} {
		if key == other {
			t.Fatal("catalog key does not isolate scope/filter/pagination")
		}
	}
}

func TestCatalogCacheDoesNotCacheLegacyWithoutRevision(t *testing.T) {
	d := cacheDocument()
	d.Revision = 0
	next := d
	next.Title = "Без ревизии, но изменён"
	s, commands := cacheMongoStore(t, ratingMongoBatch(CollectionName, d), ratingMongoBatch(CollectionName, d), ratingMongoBatch(CollectionName, next), ratingMongoBatch(CollectionName, next))
	filter := bson.M{"organization_id": d.OrganizationID}
	if _, e := s.List(context.Background(), filter, 0, 20); e != nil {
		t.Fatal(e)
	}
	items, e := s.List(context.Background(), filter, 0, 20)
	if e != nil || len(items) != 1 || items[0].Title != next.Title || len(*commands) != 4 {
		t.Fatal("legacy payload with unchanged zero revision was cached", items, e)
	}
}

func TestStoreWritesInvalidateMutableCachesAndRetainImmutableVersions(t *testing.T) {
	for _, mode := range []string{"insert", "save", "enqueue", "stale"} {
		t.Run(mode, func(t *testing.T) {
			d := cacheDocument()
			reply := bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: 1}, {Key: "nModified", Value: 1}}
			if mode == "stale" {
				reply = bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: 0}, {Key: "nModified", Value: 0}}
			}
			s, commands := cacheMongoStore(t, reply)
			cache := s.cache()
			keys := []string{documentCacheKey(7, d.ID, 1), catalogCacheKey(7, bson.M{"organization_id": int64(7)}, 0, 20), contentCacheKey(7, d.ContentVersionID), documentCacheKey(8, d.ID, 1), catalogCacheKey(8, bson.M{"organization_id": int64(8)}, 0, 20)}
			for _, key := range keys {
				cache.put(key, []byte("payload"), time.Minute, cache.epoch())
			}
			var e error
			switch mode {
			case "insert":
				_, _, e = s.Insert(context.Background(), d)
			case "save", "stale":
				_, e = s.Save(context.Background(), d, 1, 23)
			case "enqueue":
				_, e = s.EnqueueSave(context.Background(), d, 1, 23, NewEvent("tests.content.changed", map[string]any{"test_id": d.ID}))
			}
			if mode == "stale" {
				var api *APIError
				if !errors.As(e, &api) || api.Code != 409 || api.Message != "stale_revision" {
					t.Fatal("CAS mismatch did not report conflict", e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			for i, key := range keys {
				_, hit := cache.get(key)
				if hit != (i >= 2) {
					t.Fatal("mutation invalidation lost scope or immutable content", mode, key, hit)
				}
			}
			if len(*commands) != 1 {
				t.Fatal("mutation unexpectedly read cached data")
			}
		})
	}
}

func TestImmutableContentAndStudentQuestionsAreCopiedScopedAndSanitized(t *testing.T) {
	d := cacheDocument()
	v := ContentVersion{ID: d.ContentVersionID, OrganizationID: d.OrganizationID, TestID: d.ID, Questions: d.Questions,
		Batch: &ChangeBatch{Before: ContentSnapshot{Questions: d.Questions}}}
	s, commands := cacheMongoStore(t, ratingMongoBatch(ContentCollectionName, v), ratingMongoBatch(ContentCollectionName))
	version, e := s.LoadContent(context.Background(), 7, v.ID)
	if e != nil {
		t.Fatal(e)
	}
	version.Questions[0].Answers[0].Text = "client mutation"
	version.Batch.Before.Questions[0].Text = "batch mutation"
	questions, e := s.LoadSafeQuestions(context.Background(), 7, v.ID)
	if e != nil || len(questions) != 1 || questions[0].Answers[0].Text != "Верно" {
		t.Fatal(questions, e)
	}
	questions[0].Answers[0].Text = "student mutation"
	again, e := s.LoadSafeQuestions(context.Background(), 7, v.ID)
	if e != nil || again[0].Answers[0].Text != "Верно" {
		t.Fatal("safe questions share slices", again, e)
	}
	raw, hit := s.cache().get(safeContentCacheKey(7, v.ID))
	if !hit || strings.Contains(string(raw), "is_correct") || strings.Contains(string(raw), "correct_answers") || strings.Contains(string(raw), "change_batch") {
		t.Fatal("sanitized cache contains grading keys/history", string(raw))
	}
	againVersion, e := s.LoadContent(context.Background(), 7, v.ID)
	if e != nil || againVersion.Batch.Before.Questions[0].Text != "Вопрос" {
		t.Fatal("immutable batch shares nested values", againVersion, e)
	}
	if _, e = s.LoadSafeQuestions(context.Background(), 8, v.ID); !errors.Is(e, missing) {
		t.Fatal("version cache leaked into another organization", e)
	}
	if len(*commands) != 2 {
		t.Fatal("immutable/sanitized hits reread Mongo", len(*commands))
	}
	for i, command := range *commands {
		org := int64(7 + i)
		if command.raw.Lookup("filter").Document().Lookup("organization_id").Int64() != org {
			t.Fatal("immutable query lost organization scope", command.raw)
		}
	}
}

func TestContentCacheSeparatesVersionIDsAndExpires(t *testing.T) {
	first := ContentVersion{ID: bson.NewObjectID(), OrganizationID: 7, Questions: []Question{{QuestionID: 1, Type: "text", Text: "Первая версия", Points: 1, CorrectAnswers: []string{"secret alias"}}}}
	second := ContentVersion{ID: bson.NewObjectID(), OrganizationID: 7, Questions: []Question{{QuestionID: 1, Type: "text", Text: "Вторая версия", Points: 2, CorrectAnswers: []string{"another secret alias"}}}}
	store, commands := cacheMongoStore(t, ratingMongoBatch(ContentCollectionName, first), ratingMongoBatch(ContentCollectionName, second), ratingMongoBatch(ContentCollectionName, first))
	now := time.Unix(100, 0)
	store.cache().now = func() time.Time { return now }
	for _, version := range []ContentVersion{first, second, first} {
		questions, e := store.LoadSafeQuestions(context.Background(), 7, version.ID)
		if e != nil || len(questions) != 1 || questions[0].Text != version.Questions[0].Text || questions[0].Points != version.Questions[0].Points {
			t.Fatal("cache mixed immutable versions", questions, e)
		}
		raw, _ := store.cache().get(safeContentCacheKey(7, version.ID))
		if strings.Contains(string(raw), "secret alias") || strings.Contains(string(raw), "correct_answers") {
			t.Fatal("student cache leaked text answer aliases", string(raw))
		}
	}
	if len(*commands) != 2 {
		t.Fatal("immutable version hit reread Mongo", len(*commands))
	}
	now = now.Add(contentTTL)
	if _, e := store.LoadSafeQuestions(context.Background(), 7, first.ID); e != nil || len(*commands) != 3 {
		t.Fatal("immutable and sanitized TTL did not expire together", e, len(*commands))
	}
}
