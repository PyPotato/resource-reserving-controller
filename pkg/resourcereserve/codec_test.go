package resourcereserve

import (
    "reflect"
    "testing"
)

func TestDeserializeStringSlice(t *testing.T) {
    tests := []struct {
        name    string
        data    string
        want    []string
        wantErr bool
    }{
        {
            name:    "valid input",
            data:    "zsj-dev-worker1-201",
            want:    []string{"zsj-dev-master-200"},
            wantErr: false,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := deserializeStringSlice(tt.data)
            if (err != nil) != tt.wantErr {
                t.Errorf("deserializeStringSlice() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("deserializeStringSlice() = %v, want %v", got, tt.want)
            }
        })
    }
}

func TestSerializeStringSlice(t *testing.T) {
    tests := []struct {
        name    string
        data    []string
        want    string
        wantErr bool
    }{
        {
            name:    "valid input",
            data:    []string{"zsj-dev-worker1-201", "zsj-dev-master-200"},
            want:    `["zsj-dev-worker1-201","zsj-dev-master-200"]`,
            wantErr: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := serializeStringSlice(tt.data)
            if (err != nil) != tt.wantErr {
                t.Errorf("serializeStringSlice() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if got != tt.want {
                t.Errorf("serializeStringSlice() = %v, want %v", got, tt.want)
            }
        })
    }
}