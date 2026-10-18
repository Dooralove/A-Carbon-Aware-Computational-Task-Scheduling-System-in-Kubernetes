package energy

import "testing"

func TestLinearPowerModelTableT2_2(t *testing.T) {
	m := LinearPowerModel{PowerIdleWatts: 40, PowerMaxWatts: 100}

	tests := []struct {
		name string
		got  float64
		want float64
		note string
	}{
		{
			name: "14.1 P(0.5)",
			got:  m.PredictNodePower(0.5),
			want: 70,
			note: "P = 40 + 60*0.5 = 70 W",
		},
		{
			name: "14.1 P(0)",
			got:  m.PredictNodePower(0),
			want: 40,
			note: "P(0) = P_idle",
		},
		{
			name: "14.1 P(1)",
			got:  m.PredictNodePower(1),
			want: 100,
			note: "P(1) = P_max",
		},
		{
			name: "G-01 u<0",
			got:  m.PredictNodePower(-0.3),
			want: 40,
			note: "T2.2 edge: P(-0.3)=P_idle",
		},
		{
			name: "G-01 u>1",
			got:  m.PredictNodePower(1.2),
			want: 100,
			note: "clip u=1 → P_max",
		},
		{
			name: "14.1 P_pod empty-node share",
			got:  m.PredictPodAttribution(300, 1000, 100, 40),
			want: 18,
			note: "(100-40)*0.3 = 18 W (2.4a)",
		},
		{
			name: "14.1 P_pod from struct fields",
			got:  m.PredictPodAttribution(300, 1000, 0, 0),
			want: 18,
			note: "nodePower=idlePower=0 → struct P_max/P_idle",
		},
		{
			name: "14.1 P_pod with existing load (2.4)",
			got:  m.PredictPodMarginal(0.5, 0.8),
			want: 18,
			note: "(100-40)*(0.8-0.5)=18 W",
		},
		{
			name: "14.5 clip u+=1",
			got:  m.PredictPodMarginal(0.9, 1.2),
			want: 6,
			note: "u+=1, u-=0.9 → 60*0.1=6 W",
		},
		{
			name: "G-02 allocatable 0",
			got:  m.PredictPodAttribution(300, 0, 100, 40),
			want: 60,
			note: "share=1 → P_max-P_idle",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("%s: got %v, want %v (%s)", tc.name, tc.got, tc.want, tc.note)
			}
		})
	}
}

func TestLinearPowerModelImplementsPowerModel(t *testing.T) {
	var _ PowerModel = LinearPowerModel{}
	var _ PowerModel = ConstantPower{Watts: 18}
	if (ConstantPower{Watts: 18}).PredictNodePower(0.99) != 18 {
		t.Fatal("ConstantPower must ignore utilization (2.4b)")
	}
}
