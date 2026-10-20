package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

var (
	r1 = uuid.MustParse("a1000000-0000-4000-8000-000000000001")
	r4 = uuid.MustParse("a1000000-0000-4000-8000-000000000004")
)

func user(status UserStatus, roles []UserRole, rs ...uuid.UUID) User {
	return User{ID: uuid.New(), Status: status, Roles: roles, Restaurants: rs}
}

func TestAuthorizeMatrix(t *testing.T) {
	customer := user(StatusActive, []UserRole{RoleCustomer})
	staff4 := user(StatusActive, []UserRole{RoleStaff}, r4)
	owner4 := user(StatusActive, []UserRole{RoleFranchiseOwner}, r4)
	hq := user(StatusActive, []UserRole{RoleHQStaff})
	blockedHQ := user(StatusBlocked, []UserRole{RoleHQStaff})

	local := func(r uuid.UUID) AuthzRequest {
		return AuthzRequest{Action: ActionPromotionManage, Scope: ScopeLocal, RestaurantID: &r}
	}
	national := AuthzRequest{Action: ActionPromotionManage, Scope: ScopeNational}
	manage := func(r uuid.UUID) AuthzRequest { return AuthzRequest{Action: ActionOrderManage, RestaurantID: &r} }

	cases := []struct {
		name   string
		u      User
		req    AuthzRequest
		allow  bool
		reason string
	}{
		{"owner manages local promo of own restaurant", owner4, local(r4), true, ""},
		{"owner cannot manage another restaurant", owner4, local(r1), false, ReasonNotBoundToRestaurant},
		{"owner cannot widen to national", owner4, national, false, ReasonScopeNotAllowed},
		{"hq manages national", hq, national, true, ""},
		{"hq manages any local", hq, local(r1), true, ""},
		{"staff cannot manage promotions", staff4, local(r4), false, ReasonRoleRequired},
		{"customer cannot manage national", customer, national, false, ReasonRoleRequired},
		{"staff manages orders of own restaurant", staff4, manage(r4), true, ""},
		{"staff of another restaurant is denied", staff4, manage(r1), false, ReasonNotBoundToRestaurant},
		{"owner manages orders of own restaurant", owner4, manage(r4), true, ""},
		{"hq manages orders anywhere", hq, manage(r1), true, ""},
		{"customer cannot manage orders", customer, manage(r4), false, ReasonRoleRequired},
		{"blocked hq is denied national", blockedHQ, national, false, ReasonNotActive},
		{"blocked hq is denied orders", blockedHQ, manage(r4), false, ReasonNotActive},
	}
	for _, c := range cases {
		d, err := Authorize(c.u, c.req)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if d.Allowed != c.allow || d.Reason != c.reason {
			t.Errorf("%s: got %+v, want allow=%v reason=%q", c.name, d, c.allow, c.reason)
		}
	}
}

func TestAuthorizeRejectsMalformedQuestions(t *testing.T) {
	hq := user(StatusActive, []UserRole{RoleHQStaff})
	for name, req := range map[string]AuthzRequest{
		"local without restaurant":    {Action: ActionPromotionManage, Scope: ScopeLocal},
		"promotion without scope":     {Action: ActionPromotionManage},
		"promotion with wrong scope":  {Action: ActionPromotionManage, Scope: "galaxy"},
		"order.manage w/o restaurant": {Action: ActionOrderManage},
	} {
		if _, err := Authorize(hq, req); !errors.Is(err, ErrInvalidAuthz) {
			t.Errorf("%s: want ErrInvalidAuthz, got %v", name, err)
		}
	}
	if _, err := Authorize(hq, AuthzRequest{Action: "nuke"}); !errors.Is(err, ErrUnknownAction) {
		t.Errorf("unknown action: %v", err)
	}
}
