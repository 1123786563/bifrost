import { CircuitBoard } from "lucide-react";
import ContactUsView from "../views/contactUsView";
import { useTranslation } from "react-i18next";

export default function CircuitBreakerView() {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<CircuitBoard className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("errors:upsell.circuitBreaker.title")}
				description={t("errors:upsell.circuitBreaker.description")}
				readmeLink="https://docs.getbifrost.ai/enterprise/circuit-breaker"
			/>
		</div>
	);
}