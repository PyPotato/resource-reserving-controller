package resourcereserve

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
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
	for i, reservation := range *reservedResources {
		if reservation.OwnerUID == ownerUid {
			for j, resource := range reservation.ReservedResources {
				resourceType := resource.ResourceType
				reservedQuanty := resource.ReservedQuantity

				if resourceType == "cpu" {
					totalCPURequest := int64(0)
					for _, container := range pod.Spec.Containers {
						if cpuQuantity, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
							totalCPURequest += cpuQuantity.MilliValue() // 使用 MilliValue 处理 CPU 请求量
						}
					}
					reservedQuanty -= totalCPURequest
				} else if resourceType == "memory" {
					totalMemoryRequest := int64(0)
					for _, container := range pod.Spec.Containers {
						if memoryQuantity, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
							totalMemoryRequest += memoryQuantity.Value() // 使用 Value 处理内存请求量
						}
					}
					reservedQuanty -= totalMemoryRequest
				}

				// Update the reserved quantity
				(*reservedResources)[i].ReservedResources[j].ReservedQuantity = reservedQuanty
			}
		}
	}
}
