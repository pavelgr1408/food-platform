import type { components as Auth } from "./generated/auth";
import type { components as Delivery } from "./generated/delivery";
import type { components as Loyalty } from "./generated/loyalty";
import type { components as Menu } from "./generated/menu";
import type { components as Notification } from "./generated/notification";
import type { components as Orders } from "./generated/orders";
import type { components as Payment } from "./generated/payment";
import type { components as Restaurant } from "./generated/restaurant";

export type Money = Orders["schemas"]["Money"];

export type TokenPair = Auth["schemas"]["TokenPair"];
export type Principal = Auth["schemas"]["Principal"];
export type RegisteredUser = Auth["schemas"]["RegisteredUser"];
export type Role = Auth["schemas"]["Role"];

export type RestaurantSummary = Restaurant["schemas"]["RestaurantSummary"];
export type RestaurantDetails = Restaurant["schemas"]["Restaurant"];
export type RestaurantPage = Restaurant["schemas"]["RestaurantPage"];
export type WorkingHours = Restaurant["schemas"]["WorkingHours"];
export type ScheduleException = Restaurant["schemas"]["ScheduleException"];
export type RestaurantSearchParams = {
  lat?: number; lon?: number; address?: string; deliveryOnly?: boolean; openNow?: boolean;
  radiusMeters?: number; limit?: number; offset?: number;
};

export type MenuData = Menu["schemas"]["Menu"];
export type MenuCategory = Menu["schemas"]["MenuCategory"];
export type MenuItem = Menu["schemas"]["MenuItem"];
export type Modifier = Menu["schemas"]["Modifier"];
export type MenuValidationResult = Menu["schemas"]["MenuValidationResult"];
export type MenuValidationLine = Menu["schemas"]["MenuValidationLine"];

export type PromotionPublic = Loyalty["schemas"]["PromotionPublic"];

export type Cart = Orders["schemas"]["Cart"];
export type CartItem = Orders["schemas"]["CartItem"];
export type CartItemInput = Orders["schemas"]["CartItemInput"];
export type ContactInfo = Orders["schemas"]["ContactInfo"];
export type FulfillmentInput = Orders["schemas"]["FulfillmentInput"];
export type FulfillmentType = Orders["schemas"]["FulfillmentType"];
export type Order = Orders["schemas"]["Order"];
export type OrderStatus = Orders["schemas"]["OrderStatus"];
export type OrderCreateRequest = Orders["schemas"]["OrderCreateRequest"];
export type Eta = Orders["schemas"]["Eta"];
export type UnavailableItem = Orders["schemas"]["UnavailableItem"];
export type PaymentMethodType = Orders["schemas"]["PaymentMethodType"];
export type PaymentStatus = Orders["schemas"]["PaymentStatus"];
export type AppliedPromotion = Orders["schemas"]["AppliedPromotion"];

export type PaymentMethod = Payment["schemas"]["PaymentMethod"];
export type Payment_ = Payment["schemas"]["Payment"];

export type DeliveryInfo = Delivery["schemas"]["DeliveryInfo"];
export type CourierLocation = Delivery["schemas"]["CourierLocation"];

export type DeviceToken = Notification["schemas"]["DeviceToken"];
export type DeviceTokenRegistration = Notification["schemas"]["DeviceTokenRegistration"];
