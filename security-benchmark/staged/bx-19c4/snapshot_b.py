def execute(data):
    trusted = data["sender_ok"] and data["source_route"] == data["expected_route"]
    return {"released": data["amount"] if trusted else 0}
