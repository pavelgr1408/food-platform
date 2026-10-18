package seed

import "testing"

func TestEmbeddedSeedIsValid(t *testing.T) {
	rs, err := Restaurants()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) < 5 {
		t.Fatalf("restaurants: %d", len(rs))
	}
	active := 0
	for _, r := range rs {
		if r.Active {
			active++
			if len(r.Schedule.Hours) == 0 {
				t.Errorf("%s: no working hours", r.Name)
			}
		}
	}
	if active == len(rs) {
		t.Error("seed must contain an inactive restaurant (404 scenario)")
	}
}

func TestParseRejectsBadData(t *testing.T) {
	bad := []string{
		`[{"id":"x","name":"n","timezone":"Europe/Moscow"}]`,
		`[{"id":"a1000000-0000-4000-8000-000000000001","name":"n","timezone":"Mars/Base","deliveryEnabled":false}]`,
		`[{"id":"a1000000-0000-4000-8000-000000000001","name":"n","timezone":"Europe/Moscow","deliveryEnabled":true,"deliveryRadiusMeters":0}]`,
		`[{"id":"a1000000-0000-4000-8000-000000000001","name":"n","timezone":"Europe/Moscow","deliveryEnabled":false,"hours":[{"days":[8],"opensAt":"09:00","closesAt":"10:00"}]}]`,
		`[{"id":"a1000000-0000-4000-8000-000000000001","name":"n","timezone":"Europe/Moscow","deliveryEnabled":false,"hours":[{"days":[1],"opensAt":"09:00","closesAt":"09:00"}]}]`,
		`[{"id":"a1000000-0000-4000-8000-000000000001","name":"n","timezone":"Europe/Moscow","deliveryEnabled":false,"hours":[{"days":[1],"opensAt":"24:00","closesAt":"10:00"}]}]`,
	}
	for i, b := range bad {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}
