package resourcereserve

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

func deserializeStringSlice(data string) ([]string, error) {
	var slice []string
	err := json.Unmarshal([]byte(data), &slice)
	if err != nil {
		return nil, err
	}
	return slice, nil
}

func serializeStringSlice(slice []string) (string, error) {
	data, err := json.Marshal(slice)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func updateReservedResources(reservedResources *[]ReservationItem, ownerUid string, pod *corev1.Pod) {
	totalCPURequest := int64(0)
	totalMemoryRequest := int64(0)
	for _, container := range pod.Spec.Containers {
		totalCPURequest += container.Resources.Requests.Cpu().MilliValue()
		totalMemoryRequest += container.Resources.Requests.Memory().Value()
	}
	for i, reservation := range *reservedResources {
		if reservation.OwnerUID == ownerUid {
			for j, resource := range reservation.ReservedResources {
				resourceType := resource.ResourceType
				reservedQuanty := resource.ReservedQuantity

				if resourceType == "cpu" {
					reservedQuanty -= totalCPURequest
				} else if resourceType == "memory" {
					reservedQuanty -= totalMemoryRequest
				}
				// Update the reserved quantity
				(*reservedResources)[i].ReservedResources[j].ReservedQuantity = reservedQuanty
			}
			break
		}
	}
}

func printReservedResources(reservation *ReservationItem) {
	for _, resource := range reservation.ReservedResources {
		resourceType := resource.ResourceType
		reservedQuanty := resource.ReservedQuantity
		klog.Info("resourceType: ", resourceType, "; reservedQuanty: ", reservedQuanty)
	}
}
