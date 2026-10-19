package httpapi_test

import (
	"fmt"

	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
)

type clients_info = clients.RestaurantInfo

var errUnavailable = fmt.Errorf("%w: menu down", clients.ErrUnavailable)
