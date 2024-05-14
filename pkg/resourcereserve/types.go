package resourcereserve

type ReservationItem struct {
	OwnerType         string            `json:"owner_type"`
	OwnerUID          string            `json:"owner_uid"`
	PodName           string            `json:"pod_name"`
	ReservedResources []ReservedResource `json:"reserved_resources"`
}

type ReservedResource struct {
	ResourceType    string `json:"resource_type"`
	ReservedQuantity int64 `json:"reserved_quanty"`
}